package models

import (
	"github.com/aekis/goql/pkg/models"
	"github.com/aekis/goql/pkg/orm"
)

type Tag struct {
	orm.Model
	Name string
}

func init() {
	err := models.AddModel(
		&Tag{},
		"tags",
		&models.Field{
			Name: "Name",
			Type: "varchar(255)",
			Checks: []string{
				"length(name) > 2",
			},
			Index:   "idx_name",
			NotNull: true,
		},
	)
	if err != nil {
		panic(err)
	}
}
