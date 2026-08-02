// Regenerate the compiled lambda registry used by `-tags prod` builds.
//go:generate go run ../../tools/goqlc .

// Command demo exercises the goql API against a scratch SQLite database.
//
// Every lambda below is *parsed from its source*, never executed: the statements in a
// body describe the SQL to generate. That is why the entity parameter is a pointer —
// the body reads as the mutation the generated statement performs.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"

	"github.com/aekis-dev/goql"
	"github.com/aekis-dev/goql/tests/models"
	_ "github.com/mattn/go-sqlite3"
)

func main() {
	os.Remove("./demo.db")
	db, err := sql.Open("sqlite3", "./demo.db")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	ctx := context.Background()
	e := goql.New(db, goql.SQLite{})
	if err := e.EnableForeignKeys(); err != nil {
		log.Fatal("enable foreign keys: ", err)
	}

	fmt.Println("Creating tables...")
	if err := e.CreateTables(&models.Customer{}, &models.Order{}, &models.Tag{}, &models.OrderArchive{},
		&models.Category{}); err != nil {
		log.Fatal("create tables: ", err)
	}

	// --- Create: results come back typed, no casts needed.
	tags, err := goql.Create(ctx, e, []models.Tag{
		{Name: "urgent"}, {Name: "vip"}, {Name: "fragile"},
	})
	if err != nil {
		log.Fatal("create tags: ", err)
	}
	fmt.Printf("Created %d tags\n", len(tags))

	customers, err := goql.Create(ctx, e, []models.Customer{
		{Name: "Alice", Age: 40, Number: 10, Country: "USA", Status: "Active", Login: "alice"},
		{Name: "Bob", Age: 41, Number: 20, Country: "Canada", Status: "Active", Login: "bob"},
	})
	if err != nil {
		log.Fatal("create customers: ", err)
	}
	fmt.Printf("Created %d customers\n", len(customers))

	orders, err := goql.Create(ctx, e, []models.Order{
		{
			Total: 1500, Priority: "Normal", ShippingMethod: "Standard",
			Customer: customers[0],
			Tags:     []models.Tag{*tags[0], *tags[1]},
		},
		{
			Total: 700, Priority: "High", ShippingMethod: "Overnight",
			Customer: customers[1],
			Tags:     []models.Tag{*tags[2]},
		},
	})
	if err != nil {
		log.Fatal("create orders: ", err)
	}
	fmt.Printf("Created %d orders\n", len(orders))

	// --- Search by example: non-zero fields become the WHERE clause.
	found, err := goql.Search(ctx, e, models.Customer{Country: "USA"})
	if err != nil {
		log.Fatal("search by example: ", err)
	}
	fmt.Printf("Found %d US customers (first: %s)\n", len(found), found[0].Name)

	// --- Update through a relation: the predicate reaches into Customer, which becomes
	// a JOIN in the generated UPDATE.
	rows, err := goql.Update[models.Order](ctx, e, func(o *models.Order) {
		if o.Customer.Country == "USA" && o.Total > 1000 {
			o.Priority = "High"
			o.ShippingMethod = "Express"
		}
	})
	if err != nil {
		log.Fatal("update orders: ", err)
	}
	fmt.Printf("Upgraded %d high-value US orders\n", rows)

	// --- if/else compiles to one UPDATE per arm, each with a mutually exclusive WHERE.
	rows, err = goql.Update[models.Customer](ctx, e, func(c *models.Customer) {
		if c.Age > 40 {
			c.Status = "Senior"
		} else {
			c.Status = "Premium"
		}
	})
	if err != nil {
		log.Fatal("tier customers: ", err)
	}
	fmt.Printf("Tiered %d customers\n", rows)

	// --- Select with a predicate.
	highValue, err := goql.Select[models.Order](ctx, e, func(o *models.Order) bool {
		return o.Priority == "High" && o.Total > 1000
	})
	if err != nil {
		log.Fatal("select orders: ", err)
	}
	fmt.Printf("Found %d high-priority orders over 1000\n", len(highValue))

	// --- Relation traversal: a filter over a collection becomes a correlated EXISTS.
	tagged, err := goql.Select[models.Order](ctx, e, func(o *models.Order) bool {
		return goql.Filter(o.Tags, func(t *models.Tag) bool { return t.Name == "urgent" })
	})
	if err != nil {
		log.Fatal("select tagged: ", err)
	}
	fmt.Printf("Found %d orders tagged urgent\n", len(tagged))

	bigSpenders, err := goql.Select[models.Customer](ctx, e, func(c *models.Customer) bool {
		return goql.Filter(c.Orders, func(o *models.Order) bool { return o.Total > 1000 })
	})
	if err != nil {
		log.Fatal("select big spenders: ", err)
	}
	fmt.Printf("Found %d customers with a big order\n", len(bigSpenders))

	// --- Condition covers what Go operators cannot spell: LIKE, IN, IS NULL.
	named, err := goql.Select[models.Customer](ctx, e, func(c *models.Customer) bool {
		return goql.Condition(c.Name, "LIKE", "Ali%") &&
			goql.Condition(c.Country, "IN", "USA", "Canada")
	})
	if err != nil {
		log.Fatal("select by condition: ", err)
	}
	fmt.Printf("Found %d customers matching name and country\n", len(named))

	// --- Params carry values that are only known at the call site.
	type MinAge struct{ Value int }
	older, err := goql.Select[models.Customer](ctx, e, func(c *models.Customer, m MinAge) bool {
		return c.Age > m.Value
	}, MinAge{Value: 40})
	if err != nil {
		log.Fatal("select by params: ", err)
	}
	fmt.Printf("Found %d customers over 40\n", len(older))

	// --- Insert … SELECT: build rows in the database from rows already there. The first
	// lambda parameter is the destination, the second the source.
	archived, err := goql.Insert[models.OrderArchive](ctx, e,
		func(a *models.OrderArchive, o *models.Order) {
			if o.Total > 1000 {
				a.Total = o.Total
				a.Reason = "high value"
				a.Origin = o.ShippingMethod
			}
		})
	if err != nil {
		log.Fatal("archive orders: ", err)
	}
	fmt.Printf("Archived %d orders\n", archived)

	// --- Options declared as lambda parameters, which the prod registry must carry too.
	newest, err := goql.Select[models.Order](ctx, e,
		func(o *models.Order, sort *goql.Sort, limit *goql.Limit) bool {
			sort.By = "Total"
			sort.Desc = true
			limit.Value = 1
			return o.Total > 0
		})
	if err != nil {
		log.Fatal("select newest: ", err)
	}
	fmt.Printf("Top order by total: %d row(s), first total %.2f\n", len(newest), newest[0].Total)

	// --- Joining a model with no declared relation: a comparison between two declared
	// models is the join condition, and both tables enter the FROM clause.
	if err := e.CreateTables(&models.Invoice{}, &models.Payment{}); err != nil {
		log.Fatal("create ledger tables: ", err)
	}
	if _, err := goql.Create(ctx, e, []models.Invoice{{Ref: "A-1", Amount: 100, Status: "open"}}); err != nil {
		log.Fatal("create invoice: ", err)
	}
	if _, err := goql.Create(ctx, e, []models.Payment{{Ref: "A-1", Amount: 100, Method: "card"}}); err != nil {
		log.Fatal("create payment: ", err)
	}
	paid, err := goql.Select[models.Invoice](ctx, e, func(i *models.Invoice, p *models.Payment) bool {
		return i.Ref == p.Ref && p.Method == "card"
	})
	if err != nil {
		log.Fatal("select paid invoices: ", err)
	}
	fmt.Printf("Found %d invoices paid by card\n", len(paid))

	// An explicit join states the condition and the kind. A LEFT join keeps invoices with
	// no payment, which an equality between two models cannot express.
	allInvoices, err := goql.Select[models.Invoice](ctx, e,
		func(i *models.Invoice, p *models.Payment, j *goql.Join) bool {
			j.Model = p
			j.On = i.Ref == p.Ref
			j.Type = goql.Left
			return i.Status == "open"
		})
	if err != nil {
		log.Fatal("select invoices with a left join: ", err)
	}
	fmt.Printf("Open invoices, paid or not: %d\n", len(allInvoices))

	// A join declared by relation path: the models say how each hop relates, so only the
	// path and a handle for the far row are needed. LEFT keeps orders whose customer row
	// is missing, which a path written in a predicate cannot express.
	withCustomer, err := goql.Select[models.Order](ctx, e,
		func(o *models.Order, c *models.Customer, j *goql.Join) bool {
			j.Field = o.Customer
			j.Model = c
			j.Type = goql.Left
			return c.Country == "USA"
		})
	if err != nil {
		log.Fatal("select orders by path join: ", err)
	}
	fmt.Printf("Orders whose customer is in the USA: %d\n", len(withCustomer))

	// A field path traverses any number of many2one hops.
	deep, err := goql.Select[models.Category](ctx, e, func(c *models.Category) bool {
		return c.Parent.Parent.Name == "Root"
	})
	if err != nil {
		log.Fatal("select by deep path: ", err)
	}
	fmt.Printf("Categories two levels under Root: %d\n", len(deep))

	// --- Subquery: a goql call written inside a lambda is parsed too, and compiles to a
	// nested SELECT. Naming it makes it reusable; Unwrap nests one directly.
	usOrders, err := goql.Select[models.Order](ctx, e, func(o *models.Order) bool {
		usa, _ := goql.Select[models.Customer](ctx, e, func(c *models.Customer) bool {
			return c.Country == "USA"
		})
		return goql.Condition(o.Customer, "IN", usa)
	})
	if err != nil {
		log.Fatal("select via subquery: ", err)
	}
	fmt.Printf("Found %d orders from US customers (subquery)\n", len(usOrders))

	// A correlated EXISTS: the nested predicate refers to the outer row.
	bigBuyers, err := goql.Select[models.Customer](ctx, e, func(c *models.Customer) bool {
		return goql.Unwrap(goql.Exists[models.Order](ctx, e, func(o *models.Order) bool {
			return o.Customer == c && o.Total > 1000
		}))
	})
	if err != nil {
		log.Fatal("select via correlated exists: ", err)
	}
	fmt.Printf("Found %d customers with an order over 1000 (correlated EXISTS)\n", len(bigBuyers))

	// --- Aggregates: the result type is not a model, so the lambda projects the columns it
	// wants. The plain assignments are the GROUP BY; the rest are aggregates.
	type PriorityTotals struct {
		Priority string
		Orders   int64
		Total    float64
		Largest  float64
		Label    string
	}
	totals, err := goql.Select[PriorityTotals](ctx, e,
		func(t *PriorityTotals, o *models.Order, from *goql.From) bool {
			from.Model = o
			t.Priority = o.Priority
			t.Orders = goql.Count()
			t.Total = goql.Sum(o.Total)
			t.Largest = goql.Max(o.Total)
			// A computed column: nothing is stored, the engine evaluates it per row.
			t.Label = o.Priority + " / " + o.ShippingMethod
			return o.Total > 0
		})
	if err != nil {
		log.Fatal("aggregate order totals: ", err)
	}
	for _, row := range totals {
		fmt.Printf("Priority %s: %d order(s), total %.2f, largest %.2f, label %q\n",
			row.Priority, row.Orders, row.Total, row.Largest, row.Label)
	}

	// --- Grouping by a relation, which cannot be projected, plus a HAVING: an aggregate in
	// the predicate filters groups rather than rows.
	type CustomerSpend struct {
		Spend float64
	}
	big, err := goql.Select[CustomerSpend](ctx, e,
		func(c *CustomerSpend, o *models.Order, from *goql.From, g *goql.Group) bool {
			from.Model = o
			g.By = []string{"Customer"}
			c.Spend = goql.Sum(o.Total)
			return goql.Sum(o.Total) > 1000
		})
	if err != nil {
		log.Fatal("group by customer: ", err)
	}
	fmt.Printf("Customers spending over 1000: %d\n", len(big))

	// --- A query bound to a name and then read from is a common table expression. This is
	// how an aggregate over an aggregate is written: the average of per-customer totals.
	type CustomerTotal struct {
		Total float64
	}
	type Spend struct {
		Average float64
		Biggest float64
	}
	spend, err := goql.Select[Spend](ctx, e,
		func(s *Spend, t *CustomerTotal, from *goql.From) bool {
			totals, _ := goql.Select[CustomerTotal](ctx, e,
				func(ct *CustomerTotal, o *models.Order, f *goql.From, g *goql.Group) bool {
					f.Model = o
					g.By = []string{"Customer"}
					ct.Total = goql.Sum(o.Total)
					return o.Total > 0
				})
			from.Query = totals
			from.Model = t
			s.Average = goql.Avg(t.Total)
			s.Biggest = goql.Max(t.Total)
			return t.Total > 0
		})
	if err != nil {
		log.Fatal("summarise spend: ", err)
	}
	fmt.Printf("Average customer spend %.2f, biggest %.2f\n", spend[0].Average, spend[0].Biggest)

	// --- A recursive CTE walks a hierarchy. Recursion is stated, not inferred: the combining
	// lambda declares `t []*CatNode` — the rows produced so far — and the second branch joins
	// it. The depth column is what bounds the walk.
	if err := e.CreateTables(&models.Category{}); err != nil {
		log.Fatal("create categories: ", err)
	}
	catRoot, err := goql.Create(ctx, e, []models.Category{{Name: "root", Active: true}})
	if err != nil {
		log.Fatal("create category: ", err)
	}
	catMid, err := goql.Create(ctx, e, []models.Category{{Name: "mid", Active: true, Parent: catRoot[0]}})
	if err != nil {
		log.Fatal("create category: ", err)
	}
	if _, err := goql.Create(ctx, e, []models.Category{{Name: "leaf", Active: true, Parent: catMid[0]}}); err != nil {
		log.Fatal("create category: ", err)
	}

	type CatNode struct {
		ID    int64
		Name  string
		Depth int64
	}
	type TreeSummary struct {
		Nodes   int64
		Deepest int64
	}
	tree, err := goql.Select[TreeSummary](ctx, e,
		func(s *TreeSummary, n *CatNode, from *goql.From) bool {
			walk, _ := goql.Select[CatNode](ctx, e, func(t []*CatNode) bool {
				roots, _ := goql.Select[CatNode](ctx, e,
					func(r *CatNode, c *models.Category, f *goql.From) bool {
						f.Model = c
						r.ID = c.ID
						r.Name = c.Name
						r.Depth = 0
						return goql.Condition(c.Parent, "IS NULL")
					})
				children, _ := goql.Select[CatNode](ctx, e,
					func(r *CatNode, prev *CatNode, c *models.Category, f *goql.From, j *goql.Join) bool {
						f.Model = c
						j.Query = t
						j.Model = prev
						j.On = c.Parent.ID == prev.ID
						r.ID = c.ID
						r.Name = c.Name
						r.Depth = prev.Depth + 1
						return prev.Depth < 10
					})
				return goql.UnionAll(roots, children)
			})
			from.Query = walk
			from.Model = n
			s.Nodes = goql.Count()
			s.Deepest = goql.Max(n.Depth)
			return true
		})
	if err != nil {
		log.Fatal("walk categories: ", err)
	}
	fmt.Printf("Category tree: %d nodes, deepest level %d\n", tree[0].Nodes, tree[0].Deepest)

	// A lambda written inside a transaction closure. Keys are positional, so a nested
	// literal compiles into the registry like any other — this is what the dev/prod
	// comparison checks for that case.
	if err := e.Transaction(func(tx *goql.Engine) error {
		urgent, err := goql.Select[models.Order](ctx, tx, func(o *models.Order) bool {
			return goql.Filter(o.Tags, func(t *models.Tag) bool { return t.Name == "urgent" })
		})
		if err != nil {
			return err
		}
		fmt.Printf("Orders tagged urgent, read inside a transaction: %d\n", len(urgent))

		// Two levels deep, to prove the registry keys a doubly-nested literal too.
		return tx.Transaction(func(inner *goql.Engine) error {
			vip, err := goql.Select[models.Order](ctx, inner, func(o *models.Order) bool {
				return goql.Filter(o.Tags, func(t *models.Tag) bool { return t.Name == "vip" })
			})
			if err != nil {
				return err
			}
			fmt.Printf("Orders tagged vip, read two levels deep: %d\n", len(vip))
			return nil
		})
	}); err != nil {
		log.Fatal("transactional select: ", err)
	}

	// --- Set operations: each branch is a projection into the same result type, so the
	// compiler checks they match. Options on the outer lambda apply to the combination.
	type Movement struct {
		Ref    string
		Amount float64
	}
	movements, err := goql.Select[Movement](ctx, e, func(m *Movement, sort *goql.Sort) bool {
		sort.By = "Amount"
		sort.Desc = true

		live, _ := goql.Select[Movement](ctx, e, func(m *Movement, o *models.Order, from *goql.From) bool {
			from.Model = o
			m.Ref = o.Priority
			m.Amount = o.Total
			return o.Total > 0
		})
		archived, _ := goql.Select[Movement](ctx, e, func(m *Movement, a *models.OrderArchive, from *goql.From) bool {
			from.Model = a
			m.Ref = a.Reason
			m.Amount = a.Total
			return a.Total > 0
		})
		return goql.Union(live, archived)
	})
	if err != nil {
		log.Fatal("union movements: ", err)
	}
	fmt.Printf("Movements across live and archived orders: %d (largest %.2f)\n",
		len(movements), movements[0].Amount)

	// --- Write: modify a loaded entity and persist only what changed.
	customers[0].Nickname = "ali"
	rows, err = goql.Write(ctx, e, []models.Customer{*customers[0]})
	if err != nil {
		log.Fatal("write customer: ", err)
	}
	fmt.Printf("Wrote %d customers\n", rows)

	// --- Delete by predicate, then remove a loaded entity by key.
	rows, err = goql.Delete[models.Order](ctx, e, func(o *models.Order) bool {
		return o.Priority == "Normal"
	})
	if err != nil {
		log.Fatal("delete orders: ", err)
	}
	fmt.Printf("Deleted %d normal orders\n", rows)

	rows, err = goql.Remove(ctx, e, []models.Tag{*tags[2]})
	if err != nil {
		log.Fatal("remove tag: ", err)
	}
	fmt.Printf("Removed %d tags\n", rows)
}
