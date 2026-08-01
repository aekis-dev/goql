# Recursive queries

Walking a hierarchy — an org chart, a category tree, a bill of materials — is the one thing
with no substitute in plain SQL. goql spells it as a [CTE](cte.md) that joins itself.

## The shape

```go
type CatNode struct {
    ID    int64
    Name  string
    Depth int64
}

type TreeSummary struct {
    Nodes   int64
    Deepest int64
}

rows, err := goql.Select[TreeSummary](ctx, e,
    func(s *TreeSummary, n *CatNode, from *goql.From) bool {

        tree, _ := goql.Select[CatNode](ctx, e, func(t []*CatNode) bool {   // (1)

            roots, _ := goql.Select[CatNode](ctx, e,                        // (2)
                func(r *CatNode, c *Category, f *goql.From) bool {
                    f.Model = c
                    r.ID, r.Name, r.Depth = c.ID, c.Name, 0
                    return goql.Condition(c.Parent, "IS NULL")
                })

            children, _ := goql.Select[CatNode](ctx, e,                     // (3)
                func(r *CatNode, prev *CatNode, c *Category,
                    f *goql.From, j *goql.Join) bool {
                    f.Model = c
                    j.Query, j.Model = t, prev                              // (4)
                    j.On = c.Parent.ID == prev.ID
                    r.ID, r.Name = c.ID, c.Name
                    r.Depth = prev.Depth + 1                                // (5)
                    return prev.Depth < 5                                   // (6)
                })

            return goql.UnionAll(roots, children)                           // (7)
        })

        from.Query = tree
        from.Model = n
        s.Nodes = goql.Count()
        s.Deepest = goql.Max(n.Depth)
        return true
    })
```

1. **`t []*CatNode`** — the rows the query has produced so far. Declaring it is what makes a
   self-reference possible.
2. The **anchor**: where the walk starts.
3. The **recursive term**: what extends it.
4. **Join the CTE to itself.** This is the recursion, written down.
5. A [computed column](expressions.md) tracking depth.
6. The bound — see [Termination](#termination).
7. Anchor first. The order matters.

```sql
WITH RECURSIVE "tree" AS (
      SELECT c."id" AS "ID", c."name" AS "Name", $1 AS "Depth"
      FROM "categories" c WHERE c."parent_id" IS NULL
    UNION ALL
      SELECT c."id" AS "ID", c."name" AS "Name", (t."Depth" + $2) AS "Depth"
      FROM "categories" c INNER JOIN "tree" t ON c."parent_id" = t."ID"
      WHERE t."Depth" < $3)
SELECT COUNT(*) AS "Nodes", MAX(t."Depth") AS "Deepest" FROM "tree" t
```

## `RECURSIVE` is derived, never declared

goql marks the `WITH` recursive because a branch **joins the query being defined**. There is
no flag to set and nothing to keep in step: the join is the statement of intent.

That is also why the self-reference is a declared parameter rather than a positional
convention. Recursion appears three times in the source — `t` is declared, joined, and its
condition written out — and the three cannot disagree, because two of them *are* the join.

## The row handle

`prev *CatNode` is one row already in the CTE, paired to it by `j.Model = prev`. Use it
anywhere:

```go
j.On = c.Parent.ID == prev.ID     // in the join
r.Depth = prev.Depth + 1          // in the projection
return prev.Depth < 5             // in the predicate
```

Its columns are the **anchor's** projection — which is where SQL takes a recursive CTE's
column types from too. Joining the handle before anything has been selected into it is
refused, naming the anchor.

## Termination

Neither PostgreSQL nor SQLite has a depth guard, and **no engine allows `LIMIT` in a recursive
term**. Two tools:

- **`goql.Union` instead of `UnionAll`** — deduplicates, so a cyclic graph terminates.
- **A depth column filtered in the recursive term**, as above. `prev.Depth < 5` stops the walk
  from extending anything already at depth 5.

MySQL additionally caps recursion at `cte_max_recursion_depth` (1000 by default).

!!! danger "A cycle with `UnionAll` and no depth bound will not terminate."
    The engine will spin until it exhausts memory or hits a server limit. If the data can
    contain cycles, use `Union` or bound the depth.

## What is refused

`checkRecursive` rejects, while building, everything no engine allows in a recursive term:

| Refused | Why |
|---|---|
| aggregates in the recursive term | not allowed; aggregate over the finished query |
| `GROUP BY` there | not allowed |
| `ORDER BY` there | not allowed; order the finished query |
| `Limit` / `Offset` there | not allowed; filter a depth column instead |
| more than one self-reference | every engine allows exactly one |
| a self-referencing **anchor** | nothing has been produced yet |
| combining with `INTERSECT` / `EXCEPT` | only `UNION` and `UNION ALL` |
| an engine without `WITH` | a derived table cannot reference itself |

Each would otherwise fail at the database, against generated SQL you never wrote.

## Engine support

SQLite 3.8.3+, every PostgreSQL, MySQL 8.0+.

## A shorter example: descendants of one node

```go
type Node struct{ ID int64; Name string }

descendants, err := goql.Select[Node](ctx, e,
    func(out *Node, n *Node2, from *goql.From) bool {
        sub, _ := goql.Select[Node2](ctx, e, func(t []*Node2) bool {
            seed, _ := goql.Select[Node2](ctx, e,
                func(r *Node2, c *Category, f *goql.From, p RootID) bool {
                    f.Model = c
                    r.ID, r.Name = c.ID, c.Name
                    return c.ID == p.Value
                })
            kids, _ := goql.Select[Node2](ctx, e,
                func(r *Node2, prev *Node2, c *Category, f *goql.From, j *goql.Join) bool {
                    f.Model = c
                    j.Query, j.Model = t, prev
                    j.On = c.Parent.ID == prev.ID
                    r.ID, r.Name = c.ID, c.Name
                    return true
                })
            return goql.UnionAll(seed, kids)
        })
        from.Query, from.Model = sub, n
        out.ID, out.Name = n.ID, n.Name
        return true
    }, RootID{Value: 42})
```

Note the [params struct](params.md) reaching into the anchor: the starting node is runtime
data, so it travels the same way every other call-time value does. Without a depth column this
walk relies on the data being acyclic — use `Union` if it might not be.
