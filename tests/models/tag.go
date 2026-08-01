package models

import (
	"github.com/aekis-dev/goql"
	"github.com/aekis-dev/goql/models"
)

type Tag struct {
	goql.Model
	Name   string
	Orders []Order
}

func init() {
	err := models.AddModel(
		&Tag{},
		"tags",
		&models.Field{
			Name: "Name",
			Type: models.TypeVarchar,
			Size: 255,
			Checks: []string{
				"length(name) > 2",
			},
			Index:   "idx_name",
			NotNull: true,
		},
		// Declared Preload, so reads of a Tag bring its orders along by default.
		&models.Field{
			Name:      "Orders",
			OneToMany: &models.OneToMany{Ref: "tag_id"},
			Preload:   true,
		},
	)
	if err != nil {
		panic(err)
	}
}
