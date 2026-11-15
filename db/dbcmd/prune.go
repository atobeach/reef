// Package dbcmd holds the database's console commands, so the ORM never
// links the console.
package dbcmd

import (
	"slices"
	"strings"

	"github.com/atobeach/reef/collect"
	"github.com/atobeach/reef/console"
	"github.com/atobeach/reef/db"
)

// PruneCommand is `model:prune` over the app's prunable models.
func PruneCommand(prunables []db.Prunable) console.Command {
	return console.Func(`model:prune
		{--model=* : Names of the models to be pruned}
		{--except=* : Names of the models to be excluded from pruning}
		{--chunk=1000 : The number of models to retrieve per chunk of models to be deleted}
		{--pretend : Display the number of prunable records found instead of deleting them}`,
		"Prune models that are no longer needed",
		func(c *console.Context) error {
			only, except := c.OptionList("model"), c.OptionList("except")
			// A typo'd name would prune nothing every night, or prune the
			// model it was meant to spare.
			if name, ok := unknownPrunable(prunables, slices.Concat(only, except)); ok {
				names := collect.Map(prunables, func(p db.Prunable) string { return p.Model })

				return c.Failf("No prunable model is named %q. Prunable models: %s.", name, strings.Join(names, ", "))
			}

			models := selectPrunables(prunables, only, except)
			if len(models) == 0 {
				c.Info("No prunable models found.")

				return nil
			}

			chunk, err := c.IntOption("chunk", 1)
			if err != nil {
				return err
			}

			for _, model := range models {
				if c.Flag("pretend") {
					err = pretendPrune(c, model)
				} else {
					err = prune(c, model, chunk)
				}

				if err != nil {
					return err
				}
			}

			return nil
		})
}

// pretendPrune reports how many rows a prune would delete.
func pretendPrune(c *console.Context, model db.Prunable) error {
	count, err := model.Count(c)
	if err != nil {
		return err
	}

	if count == 0 {
		c.Infof("No prunable [%s] records found.", model.Model)
	} else {
		c.Infof("%d [%s] records will be pruned.", count, model.Model)
	}

	return nil
}

// prune deletes the rows, announcing the model only once a chunk goes.
func prune(c *console.Context, model db.Prunable, chunk int) error {
	announced := false
	_, err := model.PruneAll(c, chunk, func(total int64) {
		if !announced {
			announced = true

			c.NewLine()
			c.Infof("Pruning [%s] records.", model.Model)
		}

		c.Linef("  %s %d records", model.Model, total)
	})

	return err
}

// naming reports whether a --model or --except name names p,
// case-insensitively.
func naming(p db.Prunable) func(name string) bool {
	return func(name string) bool { return strings.EqualFold(name, p.Model) }
}

// unknownPrunable is the first of names that names no prunable model.
func unknownPrunable(all []db.Prunable, names []string) (string, bool) {
	for _, name := range names {
		if !slices.ContainsFunc(all, func(p db.Prunable) bool { return naming(p)(name) }) {
			return name, true
		}
	}

	return "", false
}

// selectPrunables applies --model / --except.
func selectPrunables(all []db.Prunable, only []string, except []string) []db.Prunable {
	var selected []db.Prunable
	for _, p := range all {
		if len(only) > 0 && !slices.ContainsFunc(only, naming(p)) {
			continue
		}

		if slices.ContainsFunc(except, naming(p)) {
			continue
		}

		selected = append(selected, p)
	}

	return selected
}
