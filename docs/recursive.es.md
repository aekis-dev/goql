# Consultas recursivas

Recorrer una jerarquía —un organigrama, un árbol de categorías, una lista de materiales— es lo
único que no tiene sustituto en SQL plano. goql lo escribe como una [CTE](cte.md) que hace
join consigo misma.

## La forma

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

1. **`t []*CatNode`** — las filas que la consulta ha producido hasta ahora. Declararlo es lo
   que hace posible la autorreferencia.
2. El **ancla**: por dónde empieza el recorrido.
3. El **término recursivo**: lo que lo extiende.
4. **Join de la CTE consigo misma.** Esto es la recursión, escrita explícitamente.
5. Una [columna calculada](expressions.md) que lleva la profundidad.
6. El límite — ver [Terminación](#terminacion).
7. El ancla primero. El orden importa.

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

## `RECURSIVE` se deriva, nunca se declara

goql marca el `WITH` como recursivo porque una rama **hace join con la consulta que se está
definiendo**. No hay ningún indicador que activar ni nada que mantener sincronizado: el join
es la declaración de intención.

Por eso también la autorreferencia es un parámetro declarado y no una convención posicional.
La recursión aparece tres veces en el código —`t` se declara, se une y se escribe su
condición— y las tres no pueden discrepar, porque dos de ellas *son* el join.

## El manejador de fila

`prev *CatNode` es una fila que ya está en la CTE, emparejada con ella por `j.Model = prev`.
Úsalo donde quieras:

```go
j.On = c.Parent.ID == prev.ID     // en el join
r.Depth = prev.Depth + 1          // en la proyección
return prev.Depth < 5             // en el predicado
```

Sus columnas son las de la proyección del **ancla** — que es también de donde SQL toma los
tipos de columna de una CTE recursiva. Hacer join con el manejador antes de haber seleccionado
nada en él se rechaza, nombrando el ancla.

## Terminación

Ni PostgreSQL ni SQLite tienen un límite de profundidad, y **ningún motor permite `LIMIT` en un
término recursivo**. Dos herramientas:

- **`goql.Union` en lugar de `UnionAll`** — elimina duplicados, así que un grafo cíclico
  termina.
- **Una columna de profundidad filtrada en el término recursivo**, como arriba.
  `prev.Depth < 5` impide que el recorrido extienda nada que ya esté a profundidad 5.

MySQL además limita la recursión con `cte_max_recursion_depth` (1000 por defecto).

!!! danger "Un ciclo con `UnionAll` y sin límite de profundidad no termina."
    El motor girará hasta agotar la memoria o alcanzar un límite del servidor. Si los datos
    pueden contener ciclos, usa `Union` o acota la profundidad.

## Qué se rechaza

`checkRecursive` rechaza, al construir la sentencia, todo lo que ningún motor permite en un
término recursivo:

| Rechazado | Por qué |
|---|---|
| agregados en el término recursivo | no está permitido; agrega sobre la consulta terminada |
| `GROUP BY` ahí | no está permitido |
| `ORDER BY` ahí | no está permitido; ordena la consulta terminada |
| `Limit` / `Offset` ahí | no está permitido; filtra una columna de profundidad |
| más de una autorreferencia | todos los motores permiten exactamente una |
| un **ancla** que se autorreferencia | todavía no se ha producido nada |
| combinar con `INTERSECT` / `EXCEPT` | solo `UNION` y `UNION ALL` |
| un motor sin `WITH` | una tabla derivada no puede referenciarse a sí misma |

Cada uno fallaría de otro modo en la base de datos, contra SQL generado que tú nunca
escribiste.

## Soporte por motor

SQLite 3.8.3+, todas las versiones de PostgreSQL, MySQL 8.0+.

## Un ejemplo más corto: descendientes de un nodo

```go
rows, err := goql.Select[TreeSummary](ctx, e,
    func(s *TreeSummary, n *CatNode, from *goql.From, p RootID) bool {
        sub, _ := goql.Select[CatNode](ctx, e, func(t []*CatNode) bool {
            seed, _ := goql.Select[CatNode](ctx, e,
                func(r *CatNode, c *Category, f *goql.From) bool {
                    f.Model = c
                    r.ID, r.Name, r.Depth = c.ID, c.Name, 0
                    return c.ID == p.Value
                })
            kids, _ := goql.Select[CatNode](ctx, e,
                func(r *CatNode, prev *CatNode, c *Category, f *goql.From, j *goql.Join) bool {
                    f.Model = c
                    j.Query, j.Model = t, prev
                    j.On = c.Parent.ID == prev.ID
                    r.ID, r.Name = c.ID, c.Name
                    r.Depth = prev.Depth + 1
                    return prev.Depth < 10
                })
            return goql.UnionAll(seed, kids)
        })
        from.Query, from.Model = sub, n
        s.Nodes = goql.Count()
        s.Deepest = goql.Max(n.Depth)
        return true
    }, RootID{Value: 42})
```

Fíjate en la [struct de parámetros](params.md) que llega hasta el ancla: el nodo de partida es
un dato de tiempo de ejecución, así que viaja igual que cualquier otro valor del sitio de
llamada. La struct se declara **una vez, en la lambda más externa**, y está en ámbito para
todos los cuerpos anidados.
