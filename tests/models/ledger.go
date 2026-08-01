package models

import (
	"github.com/aekis-dev/goql"
	"github.com/aekis-dev/goql/models"
)

// Invoice and Payment are deliberately unrelated: no relation is declared between them,
// they merely share a Ref column. That is the case multi-model lambdas exist for — a
// declared relation is already joined by traversing it (o.Customer.Country).

type Invoice struct {
	goql.Model
	Ref    string
	Amount float64
	Status string
}

type Payment struct {
	goql.Model
	Ref    string
	Amount float64
	Method string
}

func init() {
	if err := models.AddModel(
		&Invoice{},
		"invoices",
		&models.Field{Name: "Ref", NotNull: true, Index: true},
		&models.Field{Name: "Amount", Type: models.TypeDecimal, Precision: 10, Scale: 2},
		&models.Field{Name: "Status"},
	); err != nil {
		panic(err)
	}

	if err := models.AddModel(
		&Payment{},
		"payments",
		&models.Field{Name: "Ref", NotNull: true, Index: true},
		&models.Field{Name: "Amount", Type: models.TypeDecimal, Precision: 10, Scale: 2},
		&models.Field{Name: "Method"},
	); err != nil {
		panic(err)
	}
}
