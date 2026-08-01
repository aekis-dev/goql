package models

import (
	"github.com/aekis-dev/goql"
	"github.com/aekis-dev/goql/models"
)

type Order struct {
	goql.Model
	Total          float64
	Priority       string
	ShippingMethod string
	Customer       *Customer
	Tag            *Tag
	Tags           []Tag
}

func init() {
	err := models.AddModel(
		&Order{},
		"orders",
		&models.Field{
			Name:      "Total",
			Column:    "total_amount",
			Type:      models.TypeDecimal,
			Precision: 10,
			Scale:     2,
			Checks: []string{
				"total_amount > 0",
			},
			Index:   "idx_total",
			NotNull: true,
		},
		&models.Field{
			Name:    "Priority",
			Column:  "priority",
			Type:    models.TypeVarchar,
			Size:    20,
			Default: "Normal",
			Checks: []string{
				"priority IN ('Low', 'Normal', 'High', 'Urgent')",
			},
			Index:   "idx_priority",
			NotNull: true,
		},
		&models.Field{
			Name:    "ShippingMethod",
			Column:  "shipping_method",
			Type:    models.TypeVarchar,
			Size:    50,
			Default: "Standard",
			Checks: []string{
				"shipping_method IN ('Standard', 'Express', 'Overnight')",
			},
			NotNull: true,
		},
		&models.Field{
			Name:    "Customer",
			Column:  "customer_id",
			NotNull: true,
			Index:   "idx_customer_id",
		},
		// Nullable, unlike Customer, so rows can be disassociated from a Tag.
		&models.Field{
			Name:   "Tag",
			Column: "tag_id",
		},
		&models.Field{
			Name: "Tags",
			ManyToMany: &models.ManyToMany{
				Table:  "order_tags",
				Column: "order_id",
				Ref:    "tag_id",
			},
		},
	)
	if err != nil {
		panic(err)
	}
}
