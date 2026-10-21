package features_test

import (
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/cucumber/godog"

	"github.com/claudioed/returns-processing/internal/application/usecases"
	"github.com/claudioed/returns-processing/internal/application/views"
	"github.com/claudioed/returns-processing/internal/domain/disposition"
	"github.com/claudioed/returns-processing/internal/domain/returnauthorization"
	"github.com/claudioed/returns-processing/internal/domain/returnreceipt"
	"github.com/claudioed/returns-processing/internal/domain/shared"
)

// domainErrors maps the error names the feature files use to the domain
// and application sentinels.
var domainErrors = map[string]error{
	"ErrOrderLineNotAllocated":    usecases.ErrOrderLineNotAllocated,
	"ErrLineNotManifested":        returnauthorization.ErrLineNotManifested,
	"ErrQuantityExceedsPolicy":    returnauthorization.ErrQuantityExceedsPolicy,
	"ErrOpenRMAExists":            returnauthorization.ErrOpenRMAExists,
	"ErrLineAlreadyInspected":     returnreceipt.ErrLineAlreadyInspected,
	"ErrInspectedExceedsReceived": returnreceipt.ErrInspectedExceedsReceived,
}

// registerSetupSteps registers the clock, policy-declaration and local-copy
// seed steps. The seeds are the ADR 0003 consumed facts (allocations,
// manifests, product classifications) the Kafka consumers (a later step)
// will feed for real.
func registerSetupSteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^the clock is (.+)$`, func(raw string) error {
		at, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return err
		}
		w.clock.now = at.UTC()
		return nil
	})

	sc.Step(`^time advances (\d+) (minutes|hours|days)$`, func(n int, unit string) error {
		d := time.Duration(n) * time.Minute
		switch unit {
		case "hours":
			d = time.Duration(n) * time.Hour
		case "days":
			d = time.Duration(n) * 24 * time.Hour
		}
		w.clock.now = w.clock.now.Add(d)
		return nil
	})

	sc.Step(`^a policy is declared with window (\d+) days and cap (\d+) units per line$`,
		func(windowDays, maxUnits int) error {
			return declarePolicy(w, 1, windowDays, maxUnits, nil)
		})

	sc.Step(`^a policy is declared with window (\d+) days, cap (\d+) units per line and rule grade (\S+) class (\S+) outcome (\S+)$`,
		func(windowDays, maxUnits int, grade, class, outcome string) error {
			return declarePolicy(w, 2, windowDays, maxUnits, &disposition.Rule{
				Grade: shared.Grade(grade), ProductClass: class, Outcome: disposition.Outcome(outcome),
			})
		})

	sc.Step(`^order (\S+) line (\d+) is allocated sku (\S+)$`, func(orderID string, lineNo int, sku string) error {
		w.alloc.Put(orderID, int32(lineNo), views.AllocatedLine{SKU: sku})
		return nil
	})

	sc.Step(`^order (\S+) line (\d+) shipped in package (\S+)$`, func(orderID string, lineNo int, pkg string) error {
		w.manif.PutByOrderLine(orderID, int32(lineNo), views.ManifestedUnit{
			PackageID: pkg, ManifestedAt: w.clock.now,
		})
		return nil
	})

	sc.Step(`^sku (\S+) is classified with tags (.+)$`, func(sku, raw string) error {
		w.prods.Put(sku, views.ProductClass{Tags: splitList(raw), TemperatureClass: "AMBIENT", Version: 1})
		return nil
	})
}

// declarePolicy installs the standard total table (A restocks, B refurbishes,
// C liquidates, SCRAP disposes under "*", veto DISPOSE) — the version the
// scenarios redeclare (append-only: the class rule is version 2).
func declarePolicy(w *world, version, windowDays, maxUnits int, extra *disposition.Rule) error {
	rules := []disposition.Rule{
		{Grade: shared.GradeA, ProductClass: "*", Outcome: disposition.OutcomeRestock},
		{Grade: shared.GradeB, ProductClass: "*", Outcome: disposition.OutcomeRefurbish},
		{Grade: shared.GradeC, ProductClass: "*", Outcome: disposition.OutcomeLiquidate},
		{Grade: shared.GradeScrap, ProductClass: "*", Outcome: disposition.OutcomeDispose},
	}
	if extra != nil {
		rules = append(rules, *extra)
	}
	w.run(func() error {
		_, err := w.uc.declare.Execute(w.ctx, usecases.PolicyDeclaration{
			Version: int64(version), ReturnWindowDays: int32(windowDays),
			MaxUnitsPerLine: int64(maxUnits), Rules: rules,
		})
		return err
	})
	return nil
}

// registerReturnSteps registers the authorization command and its outcomes.
func registerReturnSteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^a return of (\d+) units? of order (\S+) line (\d+) for reason (\S+) is requested at site (\S+)$`,
		func(qty int, orderID string, lineNo int, reason, site string) error {
			w.run(func() error {
				rma, err := w.uc.authorize.Execute(w.ctx, usecases.ReturnRequest{
					OrderID: orderID, LineNo: int32(lineNo), Quantity: int64(qty),
					SiteID: site, ReasonCode: reason,
				})
				if err == nil {
					w.rmaID = rma.ID()
				}
				return err
			})
			return nil
		})

	sc.Step(`^the rma status is (\S+)$`, func(want string) error {
		return wantStr("rma status", w.currentReturn().Status, want)
	})

	sc.Step(`^the rma sku is (\S+)$`, func(want string) error {
		return wantStr("rma sku", w.currentReturn().SKU, want)
	})

	sc.Step(`^the rma quantity is (\d+)$`, func(want int) error {
		return wantInt("rma quantity", int(w.currentReturn().Quantity), want)
	})

	sc.Step(`^the rma window ends at (.+)$`, func(raw string) error {
		want, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return err
		}
		return wantTime("rma window ends at", w.currentReturn().WindowEndsAt, want)
	})

	sc.Step(`^the one return of order (\S+) is (\S+)$`, func(orderID, want string) error {
		list, err := w.uc.list.Execute(w.ctx, orderID)
		if err != nil {
			return err
		}
		if len(list) != 1 {
			return errors.New("order " + orderID + " has " + itoa(len(list)) + " returns, want exactly 1")
		}
		return wantStr("the order's return", list[0].ID, want)
	})

	sc.Step(`^order (\S+) has (\d+) returns?$`, func(orderID string, want int) error {
		list, err := w.uc.list.Execute(w.ctx, orderID)
		if err != nil {
			return err
		}
		return wantInt("returns of "+orderID, len(list), want)
	})

	sc.Step(`^the published rma events are (.+)$`, func(raw string) error {
		return wantStr("rma events", strings.Join(w.ev.RMAEventNames(), ","), strings.Join(splitList(raw), ","))
	})

	sc.Step(`^no rma events are published$`, func() error {
		return wantInt("rma events", len(w.ev.RMAEventNames()), 0)
	})
}

// registerReceiptSteps registers the receiving and inspection commands.
func registerReceiptSteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^a receipt is opened at site (\S+) with lines$`, func(site string, table *godog.Table) error {
		lines, header, err := receiptLines(table)
		if err != nil {
			return err
		}
		w.run(func() error {
			r, err := w.uc.open.Execute(w.ctx, usecases.ReturnReceiptInput{
				RMAID: header, SiteID: site, Lines: lines,
			})
			if err == nil {
				w.receiptID = r.ID()
			}
			return err
		})
		return nil
	})

	sc.Step(`^line (\d+) is inspected with grade (\S+) and qty (\d+)$`, func(lineNo int, grade string, qty int) error {
		w.run(func() error {
			_, err := w.uc.inspect.Execute(w.ctx, usecases.Inspection{
				ReceiptID: w.receiptID, LineNo: int32(lineNo),
				Grade: shared.Grade(grade), Quantity: int64(qty),
			})
			return err
		})
		return nil
	})

	sc.Step(`^the receipt has (\d+) lines?$`, func(want int) error {
		return wantInt("receipt lines", len(w.currentReceipt().Lines()), want)
	})

	sc.Step(`^receipt line (\d+) is (MATCHED|UNEXPECTED)$`, func(lineNo int, want string) error {
		return wantStr("receipt line "+itoa(lineNo)+" kind", string(w.currentLine(int32(lineNo)).Kind()), want)
	})

	sc.Step(`^receipt line (\d+) carries grade (\S+) for (\d+) units$`, func(lineNo int, grade string, qty int) error {
		l := w.currentLine(int32(lineNo))
		if err := wantStr("receipt line "+itoa(lineNo)+" grade", l.Grade().String(), grade); err != nil {
			return err
		}
		return wantInt("receipt line "+itoa(lineNo)+" inspected quantity", int(l.InspectedQuantity()), qty)
	})

	sc.Step(`^receipt line (\d+) is not inspected$`, func(lineNo int) error {
		if w.currentLine(int32(lineNo)).Inspected() {
			return errors.New("receipt line " + itoa(lineNo) + " is inspected, want not inspected")
		}
		return nil
	})

	sc.Step(`^the receipt rma is (\S+)$`, func(want string) error {
		if want == "none" {
			want = ""
		}
		return wantStr("receipt rma", w.currentReceipt().RMAID(), want)
	})

	sc.Step(`^the published receipt events are (.+)$`, func(raw string) error {
		return wantStr("receipt events", strings.Join(w.ev.ReceiptEventNames(), ","), strings.Join(splitList(raw), ","))
	})
}

// receiptLines turns the step's table into use-case lines: an rma cell that
// is empty marks the line UNEXPECTED; the parcel's header rma is the one
// rma the matched lines share ("" when every line is unexpected).
func receiptLines(table *godog.Table) ([]usecases.ReceiptLine, string, error) {
	if len(table.Rows) < 2 {
		return nil, "", errors.New("the receipt table needs a header row and at least one line")
	}
	col := map[string]int{}
	for i, c := range table.Rows[0].Cells {
		col[c.Value] = i
	}
	for _, name := range []string{"line", "sku", "qty", "rma"} {
		if _, ok := col[name]; !ok {
			return nil, "", errors.New("the receipt table needs a " + name + " column")
		}
	}
	var lines []usecases.ReceiptLine
	var header string
	for _, row := range table.Rows[1:] {
		lineNo, err := strconv.Atoi(row.Cells[col["line"]].Value)
		if err != nil {
			return nil, "", err
		}
		qty, err := strconv.Atoi(row.Cells[col["qty"]].Value)
		if err != nil {
			return nil, "", err
		}
		rma := row.Cells[col["rma"]].Value
		if rma != "" {
			if header != "" && rma != header {
				return nil, "", errors.New("the matched lines of one parcel must share one rma")
			}
			header = rma
		}
		lines = append(lines, usecases.ReceiptLine{
			LineNo: int32(lineNo), SKU: row.Cells[col["sku"]].Value,
			Quantity: int64(qty), RMAID: rma,
		})
	}
	return lines, header, nil
}

// registerDispositionSteps registers the override command and the
// disposition outcomes.
func registerDispositionSteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^the disposition of line (\d+) is overridden to (\S+) with reason (.+)$`,
		func(lineNo int, outcome, reason string) error {
			w.run(func() error {
				_, err := w.uc.override.Execute(w.ctx, usecases.Override{
					DispositionID: w.currentDisposition(int32(lineNo)).ID(),
					Outcome:       disposition.Outcome(outcome), Reason: reason,
				})
				return err
			})
			return nil
		})

	sc.Step(`^the disposition of line (\d+) is (\S+)$`, func(lineNo int, want string) error {
		return wantStr("disposition of line "+itoa(lineNo), w.currentDisposition(int32(lineNo)).Outcome().String(), want)
	})

	sc.Step(`^the disposition of line (\d+) supersedes (\S+) with reason (.+)$`,
		func(lineNo int, orig, reason string) error {
			d := w.currentDisposition(int32(lineNo))
			if err := wantStr("supersedes", d.Supersedes(), orig); err != nil {
				return err
			}
			return wantStr("reason", d.Reason(), reason)
		})

	sc.Step(`^the disposition of line (\d+) keeps grade (\S+) under policy version (\d+)$`,
		func(lineNo int, grade string, version int) error {
			d := w.currentDisposition(int32(lineNo))
			if err := wantStr("grade", d.Grade().String(), grade); err != nil {
				return err
			}
			return wantInt("policy version", int(d.PolicyVersion()), version)
		})

	sc.Step(`^the disposition of line (\d+) was decided at (.+)$`, func(lineNo int, raw string) error {
		want, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return err
		}
		return wantTime("decided at", w.currentDisposition(int32(lineNo)).DecidedAt(), want)
	})

	sc.Step(`^the disposition (\S+) is still (\S+)$`, func(id, want string) error {
		d, err := w.disps.FindByID(w.ctx, id)
		if err != nil {
			return err
		}
		return wantStr("disposition "+id+" outcome", d.Outcome().String(), want)
	})

	sc.Step(`^the current policy is version (\d+) with window (\d+) days and cap (\d+) units per line$`,
		func(version, windowDays, maxUnits int) error {
			p, err := w.uc.policy.Execute(w.ctx)
			if err != nil {
				return err
			}
			if err := wantInt("policy version", int(p.Version), version); err != nil {
				return err
			}
			if err := wantInt("policy window", int(p.ReturnWindowDays), windowDays); err != nil {
				return err
			}
			return wantInt("policy cap", int(p.MaxUnitsPerLine), maxUnits)
		})

	sc.Step(`^the published disposition events are (.+)$`, func(raw string) error {
		return wantStr("disposition events", strings.Join(w.ev.DispositionEventNames(), ","), strings.Join(splitList(raw), ","))
	})

	sc.Step(`^ReturnLineStaged is published (\d+) times?$`, func(want int) error {
		return wantInt("ReturnLineStaged count", countEvents(w, "ReturnLineStaged"), want)
	})
}

// registerCommandErrorSteps registers the command-failure assertion.
func registerCommandErrorSteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^the command fails with (\S+)$`, func(name string) error {
		w.errChecked = true
		if w.lastErr == nil {
			return errors.New("the last command succeeded, want " + name)
		}
		want, ok := domainErrors[name]
		if !ok {
			return errors.New("unknown error " + name + " in the scenario")
		}
		if !errors.Is(w.lastErr, want) {
			return errors.New("command error = " + w.lastErr.Error() + ", want " + name)
		}
		return nil
	})
}

func countEvents(w *world, name string) int {
	n := 0
	for _, got := range w.ev.DispositionEventNames() {
		if got == name {
			n++
		}
	}
	return n
}

func splitList(raw string) []string {
	out := []string{}
	for _, p := range strings.Split(raw, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func itoa(n int) string { return strconv.Itoa(n) }

// wantStr builds the "got X, want Y" failure of a scalar assertion.
func wantStr(what, got, want string) error {
	if got != want {
		return errors.New(what + " = " + got + ", want " + want)
	}
	return nil
}

// wantInt is wantStr for integers.
func wantInt(what string, got, want int) error {
	return wantStr(what, itoa(got), itoa(want))
}

// wantTime is wantStr for instants (compared by instant, not clock).
func wantTime(what string, got, want time.Time) error {
	if !got.Equal(want) {
		return errors.New(what + " = " + got.Format(time.RFC3339) + ", want " + want.Format(time.RFC3339))
	}
	return nil
}
