package models

import (
	"github.com/aekis-dev/goql"
	"github.com/aekis-dev/goql/models"
)

type Meta struct {
	Color string `json:"color"`
	Size  int    `json:"size"`
}

type Widget struct {
	goql.Model
	Name  string
	Meta  Meta
	Tags  []string
	Attrs map[string]any
}

func init() {
	err := models.AddModel(
		&Widget{},
		"widgets",
		&models.Field{Name: "Name", NotNull: true, Index: true},
		&models.Field{Name: "Meta", Type: models.TypeJSON},
		&models.Field{Name: "Tags", Type: models.TypeJSON},
		&models.Field{Name: "Attrs", Type: models.TypeJSON},
	)
	if err != nil {
		panic(err)
	}
}
