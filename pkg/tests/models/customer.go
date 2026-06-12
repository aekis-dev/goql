package models

import (
	"github.com/aekis-dev/goql/pkg/models"
	"github.com/aekis-dev/goql/pkg/orm"
)

type Customer struct {
	orm.Model
	Name     string
	Age      int
	Number   int
	Country  string
	Status   string
	Login    string
	Nickname string
	Discount float64
	Orders   []Order
}

func init() {
	err := models.AddModel(
		&Customer{},
		"customers",
		&models.Field{
			Name: "Name",
			Checks: []string{
				"length(name) > 2",
			},
			Index:   "idx_name",
			NotNull: true,
		},
		&models.Field{
			Name: "Age",
			Checks: []string{
				"age >= 0",
				"age <= 150",
			},
		},
		&models.Field{
			Name:    "Number",
			NotNull: true,
			Unique:  true,
		},
		&models.Field{
			Name:    "Country",
			Type:    "varchar(50)",
			NotNull: true,
			Index:   "idx_country",
		},
		&models.Field{
			Name:    "Status",
			Type:    "varchar(20)",
			Default: "Active",
			Checks: []string{
				"status IN ('Active', 'Inactive', 'Premium', 'Senior')",
			},
			Index: "idx_status",
		},
		&models.Field{
			Name:    "Login",
			Type:    "varchar(50)",
			NotNull: true,
			Unique:  true,
			Index:   "idx_login",
		},
		&models.Field{
			Name:   "Nickname",
			Type:   "varchar(50)",
			Unique: true,
		},
		&models.Field{
			Name:    "Discount",
			Type:    "decimal(5,2)",
			Default: 0.0,
			Checks: []string{
				"discount >= 0.0",
				"discount <= 1.0",
			},
		},
		&models.Field{
			Name: "Orders",
			OneToMany: &models.OneToMany{
				Ref: "customer_id",
			},
		},
	)

	if err != nil {
		panic(err)
	}
}
