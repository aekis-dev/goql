package models

import (
	"github.com/aekis-dev/goql"
	"github.com/aekis-dev/goql/models"
)

// Category is a self-referencing hierarchy, the shape a recursive CTE walks.
type Category struct {
	goql.Model
	Name     string
	Active   bool
	Parent   *Category
	Children []Category
}

func init() {
	err := models.AddModel(
		&Category{},
		"categories",
		&models.Field{
			Name:    "Name",
			Type:    models.TypeVarchar,
			Size:    50,
			NotNull: true,
		},
		&models.Field{
			Name: "Active",
			Type: models.TypeBoolean,
		},
		&models.Field{
			Name:   "Parent",
			Column: "parent_id",
		},
		&models.Field{
			Name: "Children",
			OneToMany: &models.OneToMany{
				Ref: "parent_id",
			},
		},
	)
	if err != nil {
		panic(err)
	}
}
