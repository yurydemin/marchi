package httpapi

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/session"

	"github.com/yurydemin/marchi/internal/domain"
	rulesengine "github.com/yurydemin/marchi/internal/rules"
)

// rulesPageData is the "rules" page's top-level template data.
type rulesPageData struct {
	Unlocked          bool
	Rules             []ruleResponse
	DefaultConditions domain.RuleNode
}

// registerRulesPage wires the server-rendered Rules screen (Phase 3 step
// 16): a full page (list + a visual AND/OR condition builder for creating
// a rule) plus HTMX-fragment routes under /rules/*, mirroring
// registerAccountsPage's shape. Reordering (PUT /rules/reorder) is driven
// by native HTML5 drag events in app.js, not htmx — it's a raw fetch()
// call that replaces the row list with the fragment this returns.
func registerRulesPage(app *fiber.App, vault *vaultState, store *session.Store, pages map[string]*template.Template) {
	app.Get("/rules", handleRulesPage(vault, store, pages))
	app.Post("/rules", handleRulesCreate(vault, store, pages))
	// /rules/reorder must be registered before the PUT /rules/:id below —
	// Fiber matches routes in registration order, and a param route
	// registered first would otherwise capture "reorder" as :id (surfacing
	// as a confusing "invalid id" error instead of ever reaching
	// handleRulesReorder).
	app.Put("/rules/reorder", handleRulesReorder(vault, store, pages))
	app.Get("/rules/:id", handleRuleRowView(vault, store, pages))
	app.Get("/rules/:id/edit", handleRuleRowEdit(vault, store, pages))
	app.Put("/rules/:id", handleRulesUpdate(vault, store, pages))
	app.Delete("/rules/:id", handleRulesDelete(vault, store, pages))
	app.Put("/rules/:id/toggle", handleRulesToggle(vault, store, pages))
}

// defaultRuleConditions is the create form's starting tree — a single
// top-level AND group with one leaf condition, rather than an empty tree
// (which internal/rules.Validate would reject anyway: a group needs at
// least one child) or a bare leaf with no group wrapper (technically
// valid, but a group-first UI is the more intuitive starting point for a
// builder whose whole job is adding more conditions to it).
func defaultRuleConditions() domain.RuleNode {
	return domain.RuleNode{
		Op:       domain.OpAnd,
		Children: []domain.RuleNode{{Type: domain.ConditionSubjectContains, Value: ""}},
	}
}

func handleRulesPage(vault *vaultState, store *session.Store, pages map[string]*template.Template) fiber.Handler {
	return func(c *fiber.Ctx) error {
		c.Set(fiber.HeaderContentType, fiber.MIMETextHTMLCharsetUTF8)
		b, ok := pageBackend(c, vault, store)
		if !ok {
			return renderLocked(c, pages)
		}
		resp, err := listRuleResponses(c, b)
		if err != nil {
			return err
		}
		return pages["rules"].ExecuteTemplate(c, "layout", rulesPageData{
			Unlocked: true, Rules: resp, DefaultConditions: defaultRuleConditions(),
		})
	}
}

func listRuleResponses(c *fiber.Ctx, b *backend) ([]ruleResponse, error) {
	rulesList, err := b.rulesRepo.List(c.Context())
	if err != nil {
		return nil, fiber.NewError(fiber.StatusInternalServerError, "listing rules failed")
	}
	resp := make([]ruleResponse, len(rulesList))
	for i, r := range rulesList {
		resp[i] = ruleResponseFrom(r)
	}
	return resp, nil
}

// renderRuleRows re-lists every rule (in priority order) and renders the
// "rule-rows" fragment — the same whole-tbody-replacement approach
// renderAccountRows uses, for the same reason (an empty-to-one-rule
// transition can't be handled by patching a single row when the empty
// state isn't a <tr> at all).
func renderRuleRows(c *fiber.Ctx, b *backend, pages map[string]*template.Template) error {
	resp, err := listRuleResponses(c, b)
	if err != nil {
		return err
	}
	c.Set(fiber.HeaderContentType, fiber.MIMETextHTMLCharsetUTF8)
	return pages["rules"].ExecuteTemplate(c, "rule-rows", resp)
}

// ruleFormRequest is the create/edit form's body shape — conditions
// arrive pre-serialized to JSON by app.js's htmx:configRequest listener
// (see serializeRuleNode there), since an arbitrarily nested AND/OR tree
// has no natural x-www-form-urlencoded field-name convention the way a
// flat record does. Priority isn't part of the form at all: a new rule is
// appended to the end (handleRulesCreate), an edited rule keeps its
// existing priority, and reordering is exclusively drag-and-drop's job
// (handleRulesReorder).
type ruleFormRequest struct {
	Name           string `form:"name"`
	ConditionsJSON string `form:"conditions_json"`
	Action         string `form:"action"`
	IsActive       bool   `form:"is_active"`
}

// parse validates req the same way rules_api.go's ruleRequest.validate
// does — one shared notion of "a valid rule" across the JSON API, the
// HTML form, and (internal/rules.Validate itself) rules.yaml.
func (req ruleFormRequest) parse() (domain.RuleNode, domain.RuleAction, error) {
	if strings.TrimSpace(req.Name) == "" {
		return domain.RuleNode{}, "", errors.New("name must not be empty")
	}
	action, err := domain.ParseRuleAction(req.Action)
	if err != nil {
		return domain.RuleNode{}, "", err
	}
	var conditions domain.RuleNode
	if err := json.Unmarshal([]byte(req.ConditionsJSON), &conditions); err != nil {
		return domain.RuleNode{}, "", fmt.Errorf("invalid conditions: %w", err)
	}
	if err := rulesengine.Validate(conditions); err != nil {
		return domain.RuleNode{}, "", err
	}
	return conditions, action, nil
}

// handleRulesCreate's response is exclusively the "rule-rows" fragment —
// it deliberately does NOT also re-render/reset the create form via an
// htmx out-of-band swap the way handleAccountsCreate resets
// #add-account-form. Two reasons converge here: this endpoint's
// hx-target is #rules-tbody (a <tbody>), and HTML5's table-parsing
// insertion mode foster-parents anything that isn't valid <tbody> content
// — including a <form> — out of the table structure entirely, silently
// corrupting it regardless of any hx-swap-oob id trickery. Resetting the
// form is instead app.js's job (see its htmx:afterRequest listener for
// #add-rule-form), which needs no server round-trip at all.
func handleRulesCreate(vault *vaultState, store *session.Store, pages map[string]*template.Template) fiber.Handler {
	return func(c *fiber.Ctx) error {
		b, err := requireUnlockedSession(c, vault, store)
		if err != nil {
			return err
		}
		var req ruleFormRequest
		if err := c.BodyParser(&req); err != nil {
			return ruleFragmentError(c, pages, fiber.StatusBadRequest, "invalid form submission")
		}
		conditions, action, err := req.parse()
		if err != nil {
			return ruleFragmentError(c, pages, fiber.StatusBadRequest, err.Error())
		}

		existing, err := b.rulesRepo.List(c.Context())
		if err != nil {
			return fiber.NewError(fiber.StatusInternalServerError, "listing rules failed")
		}

		rule := &domain.Rule{
			Name: req.Name, Priority: len(existing), Conditions: conditions, Action: action,
			IsActive: req.IsActive,
		}
		if _, err := b.rulesRepo.Create(c.Context(), rule); err != nil {
			return fiber.NewError(fiber.StatusInternalServerError, "creating rule failed")
		}

		return renderRuleRows(c, b, pages)
	}
}

func handleRuleRowView(vault *vaultState, store *session.Store, pages map[string]*template.Template) fiber.Handler {
	return func(c *fiber.Ctx) error {
		b, err := requireUnlockedSession(c, vault, store)
		if err != nil {
			return err
		}
		id, err := idParam(c, "id")
		if err != nil {
			return err
		}
		r, err := b.rulesRepo.GetByID(c.Context(), id)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fiber.NewError(fiber.StatusNotFound, "rule not found")
			}
			return fiber.NewError(fiber.StatusInternalServerError, "loading rule failed")
		}
		c.Set(fiber.HeaderContentType, fiber.MIMETextHTMLCharsetUTF8)
		return pages["rules"].ExecuteTemplate(c, "rule-row", ruleResponseFrom(r))
	}
}

func handleRuleRowEdit(vault *vaultState, store *session.Store, pages map[string]*template.Template) fiber.Handler {
	return func(c *fiber.Ctx) error {
		b, err := requireUnlockedSession(c, vault, store)
		if err != nil {
			return err
		}
		id, err := idParam(c, "id")
		if err != nil {
			return err
		}
		r, err := b.rulesRepo.GetByID(c.Context(), id)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fiber.NewError(fiber.StatusNotFound, "rule not found")
			}
			return fiber.NewError(fiber.StatusInternalServerError, "loading rule failed")
		}
		c.Set(fiber.HeaderContentType, fiber.MIMETextHTMLCharsetUTF8)
		return pages["rules"].ExecuteTemplate(c, "rule-edit-row", ruleResponseFrom(r))
	}
}

func handleRulesUpdate(vault *vaultState, store *session.Store, pages map[string]*template.Template) fiber.Handler {
	return func(c *fiber.Ctx) error {
		b, err := requireUnlockedSession(c, vault, store)
		if err != nil {
			return err
		}
		id, err := idParam(c, "id")
		if err != nil {
			return err
		}
		current, err := b.rulesRepo.GetByID(c.Context(), id)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fiber.NewError(fiber.StatusNotFound, "rule not found")
			}
			return fiber.NewError(fiber.StatusInternalServerError, "loading rule failed")
		}
		var req ruleFormRequest
		if err := c.BodyParser(&req); err != nil {
			return ruleFragmentError(c, pages, fiber.StatusBadRequest, "invalid form submission")
		}
		conditions, action, err := req.parse()
		if err != nil {
			return ruleFragmentError(c, pages, fiber.StatusBadRequest, err.Error())
		}

		rule := &domain.Rule{
			ID: id, Name: req.Name, Priority: current.Priority, Conditions: conditions, Action: action,
			IsActive: req.IsActive,
		}
		if err := b.rulesRepo.Update(c.Context(), rule); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fiber.NewError(fiber.StatusNotFound, "rule not found")
			}
			return fiber.NewError(fiber.StatusInternalServerError, "updating rule failed")
		}
		return renderRuleRows(c, b, pages)
	}
}

func handleRulesDelete(vault *vaultState, store *session.Store, pages map[string]*template.Template) fiber.Handler {
	return func(c *fiber.Ctx) error {
		b, err := requireUnlockedSession(c, vault, store)
		if err != nil {
			return err
		}
		id, err := idParam(c, "id")
		if err != nil {
			return err
		}
		if err := b.rulesRepo.Delete(c.Context(), id); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fiber.NewError(fiber.StatusNotFound, "rule not found")
			}
			return fiber.NewError(fiber.StatusInternalServerError, "deleting rule failed")
		}
		return renderRuleRows(c, b, pages)
	}
}

// handleRulesToggle flips is_active without touching the rule's
// conditions/action/priority — the same read-current-then-write-back
// shape handleAccountsToggle uses, for the same reason (Update replaces
// every mutable column unconditionally).
func handleRulesToggle(vault *vaultState, store *session.Store, pages map[string]*template.Template) fiber.Handler {
	return func(c *fiber.Ctx) error {
		b, err := requireUnlockedSession(c, vault, store)
		if err != nil {
			return err
		}
		id, err := idParam(c, "id")
		if err != nil {
			return err
		}
		current, err := b.rulesRepo.GetByID(c.Context(), id)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fiber.NewError(fiber.StatusNotFound, "rule not found")
			}
			return fiber.NewError(fiber.StatusInternalServerError, "loading rule failed")
		}
		current.IsActive = !current.IsActive
		if err := b.rulesRepo.Update(c.Context(), current); err != nil {
			return fiber.NewError(fiber.StatusInternalServerError, "toggling rule failed")
		}
		return renderRuleRows(c, b, pages)
	}
}

// reorderRequest is PUT /rules/reorder's body — app.js's drag-and-drop
// handler sends the rules table's rows in their new top-to-bottom order
// as plain JSON (not a form; there's no htmx involved in a drag
// gesture), so this is the one Rules UI route that parses a JSON body
// instead of form fields.
type reorderRequest struct {
	IDs []int64 `json:"ids"`
}

// handleRulesReorder re-numbers priority to match ids' order (index 0 =
// highest priority, matching RulesRepo.List's "priority ASC" ordering) —
// an id in ids that no longer exists (deleted by another session
// mid-drag) is skipped rather than failing the whole reorder, the same
// "one bad item doesn't sink the batch" precedent
// backend.runRestoreAsync/streamExport follow elsewhere.
func handleRulesReorder(vault *vaultState, store *session.Store, pages map[string]*template.Template) fiber.Handler {
	return func(c *fiber.Ctx) error {
		b, err := requireUnlockedSession(c, vault, store)
		if err != nil {
			return err
		}
		var req reorderRequest
		if err := c.BodyParser(&req); err != nil {
			return fiber.NewError(fiber.StatusBadRequest, "invalid request body")
		}
		for i, id := range req.IDs {
			rule, err := b.rulesRepo.GetByID(c.Context(), id)
			if err != nil {
				continue
			}
			rule.Priority = i
			if err := b.rulesRepo.Update(c.Context(), rule); err != nil {
				return fiber.NewError(fiber.StatusInternalServerError, "reordering rules failed")
			}
		}
		return renderRuleRows(c, b, pages)
	}
}

// ruleFragmentError is fragmentError's Rules-page counterpart: same
// HX-Retarget/HX-Reswap trick, aimed at the rule form's own error slot
// instead of the Accounts page's.
func ruleFragmentError(c *fiber.Ctx, pages map[string]*template.Template, status int, msg string) error {
	c.Status(status)
	c.Set("HX-Retarget", "#rule-form-error")
	c.Set("HX-Reswap", "innerHTML")
	c.Set(fiber.HeaderContentType, fiber.MIMETextHTMLCharsetUTF8)
	return c.SendString(template.HTMLEscapeString(msg))
}
