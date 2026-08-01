package models

import (
	"github.com/aekis-dev/goql"
	"github.com/aekis-dev/goql/models"
)

// OrderArchive is the destination model for Insert (INSERT … SELECT) tests. Reason is
// nullable so a branch that leaves it out is still insertable, and Total is deliberately
// the same logical type as Order.Total so a copy needs no conversion.
type OrderArchive struct {
	goql.Model
	Total  float64
	Reason string
	Origin string
}

func init() {
	err := models.AddModel(
		&OrderArchive{},
		"order_archives",
		&models.Field{Name: "Total", Type: models.TypeDecimal, Precision: 10, Scale: 2},
		&models.Field{Name: "Reason"},
		&models.Field{Name: "Origin", Unique: true},
	)
	if err != nil {
		panic(err)
	}
}
