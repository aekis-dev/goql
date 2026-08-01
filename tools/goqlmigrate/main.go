//go:build !prod

// Command goqlmigrate walks a schema migration interactively.
//
// It talks to the migrate socket of a running application, because only that process has
// the models registered. The socket computes the plan and applies it; this program's job is
// to show the plan, ask about anything the schema cannot answer on its own, and print what
// happened.
//
//	go run ./tools/goqlmigrate -socket /run/goql-migrate.sock -token "$GOQL_MIGRATE_TOKEN"
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/aekis-dev/goql"
)

func main() {
	socket := flag.String("socket", "", "path to the application's migrate socket")
	token := flag.String("token", "", "token the socket was configured with")
	yes := flag.Bool("yes", false, "apply without a final confirmation (questions are still asked)")
	flag.Parse()

	if *socket == "" || *token == "" {
		log.Fatal("goqlmigrate: -socket and -token are both required")
	}

	client := &socketClient{http: goql.DialMigrateSocket(*socket), token: *token}
	in := bufio.NewReader(os.Stdin)
	decisions := map[string]string{}

	// Answering one question can reveal another — a rename consumes a candidate that a
	// later question would otherwise have offered — so re-plan until nothing is left to ask.
	for {
		plan, err := client.plan(decisions)
		if err != nil {
			log.Fatalf("goqlmigrate: %v", err)
		}

		if plan.Empty() {
			fmt.Println("Schema is up to date; nothing to migrate.")
			return
		}

		if len(plan.Questions) == 0 {
			showChanges(plan)
			if !*yes && !confirm(in) {
				fmt.Println("Aborted; nothing was applied.")
				return
			}
			summary, err := client.apply(decisions)
			report(summary, err)
			return
		}

		ask(in, plan.Questions[0], decisions)
	}
}

// ask prompts for one question and records the answer.
func ask(in *bufio.Reader, q goql.Question, decisions map[string]string) {
	fmt.Printf("\n%s\n", q.Prompt)
	fmt.Println("How should it be handled?")
	for i, option := range q.Options {
		fmt.Printf("  %d) %s — %s\n", i+1, option.Label, option.Detail)
	}

	for {
		fmt.Printf("Choose 1-%d: ", len(q.Options))
		line, err := in.ReadString('\n')
		if err != nil {
			log.Fatal("goqlmigrate: no input")
		}
		choice, convErr := strconv.Atoi(strings.TrimSpace(line))
		if convErr != nil || choice < 1 || choice > len(q.Options) {
			fmt.Println("Please enter one of the listed numbers.")
			continue
		}
		decisions[q.ID] = q.Options[choice-1].Value
		return
	}
}

func showChanges(plan *goql.Plan) {
	fmt.Printf("\nPlanned changes (%s):\n", plan.Dialect)

	byTable := map[string][]goql.Change{}
	var order []string
	for _, change := range plan.Changes {
		if _, seen := byTable[change.Table]; !seen {
			order = append(order, change.Table)
		}
		byTable[change.Table] = append(byTable[change.Table], change)
	}

	for _, table := range order {
		fmt.Printf("\n  %s\n", table)
		for _, change := range byTable[table] {
			marker := " "
			if change.Destructive {
				marker = "!"
			}
			fmt.Printf("   %s %s\n", marker, change.Detail)
		}
	}

	destructive := 0
	for _, change := range plan.Changes {
		if change.Destructive {
			destructive++
		}
	}
	if destructive > 0 {
		fmt.Printf("\n  %d change(s) marked ! discard data.\n", destructive)
	}
}

func confirm(in *bufio.Reader) bool {
	fmt.Print("\nApply these changes? [y/N]: ")
	line, err := in.ReadString('\n')
	if err != nil {
		return false
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes"
}

func report(summary *goql.Summary, err error) {
	if summary != nil {
		for _, change := range summary.Applied {
			fmt.Printf("  applied: %s\n", change.Detail)
		}
	}

	if err == nil {
		fmt.Printf("\nMigration complete: %d change(s) applied.\n", len(summary.Applied))
		return
	}

	fmt.Printf("\nMigration failed: %v\n", err)
	if summary == nil {
		return
	}
	if summary.Rolled {
		fmt.Println("The engine rolled the migration back; the schema is unchanged.")
		return
	}
	if summary.Failed != nil {
		fmt.Printf("Stopped at: %s\n", summary.Failed.Detail)
	}
	fmt.Println("This engine commits each DDL statement as it runs, so the changes listed " +
		"above are already in place. Re-run to continue from here.")
	os.Exit(1)
}

// socketClient speaks the migrate socket's small JSON protocol.
type socketClient struct {
	http  *http.Client
	token string
}

func (c *socketClient) plan(decisions map[string]string) (*goql.Plan, error) {
	var plan goql.Plan
	if err := c.post("/plan", decisions, &plan); err != nil {
		return nil, err
	}
	return &plan, nil
}

func (c *socketClient) apply(decisions map[string]string) (*goql.Summary, error) {
	var response goql.ApplyResponse
	if err := c.post("/apply", decisions, &response); err != nil {
		return nil, err
	}
	if response.Error != "" {
		return response.Summary, fmt.Errorf("%s", response.Error)
	}
	return response.Summary, nil
}

func (c *socketClient) post(path string, decisions map[string]string, out any) error {
	body, err := json.Marshal(map[string]any{"decisions": decisions})
	if err != nil {
		return err
	}

	// The host is ignored: the transport dials the socket path.
	req, err := http.NewRequest(http.MethodPost, "http://goql"+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("X-Goql-Token", c.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("cannot reach the migrate socket: %w", err)
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("the socket rejected the token")
	}

	if resp.StatusCode >= http.StatusBadRequest {
		var failure struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(payload, &failure) == nil && failure.Error != "" {
			return fmt.Errorf("%s", failure.Error)
		}
		return fmt.Errorf("socket returned %s", resp.Status)
	}

	return json.Unmarshal(payload, out)
}
