package models

import (
	"github.com/aekis-dev/goql/pkg/models"
	"github.com/aekis-dev/goql/pkg/orm"
)

type Order struct {
	orm.Model
	Total          float64
	Priority       string
	ShippingMethod string
	Customer       *Customer
	Tags           []Tag
}

func init() {
	err := models.AddModel(
		&Order{},
		"orders",
		&models.Field{
			Name:   "Total",
			Column: "total_amount",
			Type:   "decimal(10,2)",
			Checks: []string{
				"total_amount > 0",
			},
			Index:   "idx_total",
			NotNull: true,
		},
		&models.Field{
			Name:    "Priority",
			Column:  "priority",
			Type:    "varchar(20)",
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
			Type:    "varchar(50)",
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
