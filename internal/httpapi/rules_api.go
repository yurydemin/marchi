package httpapi

import (
	"database/sql"
	"errors"

	"github.com/gofiber/fiber/v2"

	"github.com/yurydemin/marchi/internal/domain"
	rulesengine "github.com/yurydemin/marchi/internal/rules"
)

// registerRulesAPI wires the Rules REST API (FR-API-02, FR-RE-05): CRUD
// for archive rules, mirroring the Accounts API's shape/conventions.
// Rules created or edited here take effect on the next sync (FR-RE-01) —
// there's no separate "activate" step beyond is_active.
func registerRulesAPI(app *fiber.App, vault *vaultState) {
	app.Get("/api/v1/rules", handleListRules(vault))
	app.Post("/api/v1/rules", handleCreateRule(vault))
	app.Put("/api/v1/rules/:id", handleUpdateRule(vault))
	app.Delete("/api/v1/rules/:id", handleDeleteRule(vault))
}

type ruleResponse struct {
	ID         int64             `json:"id"`
	Name       string            `json:"name"`
	Priority   int               `json:"priority"`
	Conditions domain.RuleNode   `json:"conditions"`
	Action     domain.RuleAction `json:"action"`
	IsActive   bool              `json:"is_active"`
}

func ruleResponseFrom(r *domain.Rule) ruleResponse {
	return ruleResponse{
		ID: r.ID, Name: r.Name, Priority: r.Priority, Conditions: r.Conditions, Action: r.Action,
		IsActive: r.IsActive,
	}
}

func handleListRules(vault *vaultState) fiber.Handler {
	return func(c *fiber.Ctx) error {
		b, err := currentBackendOrLocked(vault)
		if err != nil {
			return err
		}
		rulesList, err := b.rulesRepo.List(c.Context())
		if err != nil {
			return fiber.NewError(fiber.StatusInternalServerError, "listing rules failed")
		}
		resp := make([]ruleResponse, len(rulesList))
		for i, r := range rulesList {
			resp[i] = ruleResponseFrom(r)
		}
		return c.JSON(resp)
	}
}

// ruleRequest is both the create and update request body — a rule has no
// fields (like Accounts' password) that behave differently between the
// two, so unlike createAccountRequest/updateAccountRequest there's just
// one shape here.
type ruleRequest struct {
	Name       string          `json:"name"`
	Priority   int             `json:"priority"`
	Conditions domain.RuleNode `json:"conditions"`
	Action     string          `json:"action"`
	IsActive   bool            `json:"is_active"`
}

// validate parses/checks req the same way internal/rules.ParseYAML checks
// a YAML file's rules — one shared notion of "a valid rule" regardless of
// which surface (REST, YAML) created it.
func (req ruleRequest) validate() (domain.RuleAction, error) {
	if req.Name == "" {
		return "", errors.New("name must not be empty")
	}
	action, err := domain.ParseRuleAction(req.Action)
	if err != nil {
		return "", err
	}
	if err := rulesengine.Validate(req.Conditions); err != nil {
		return "", err
	}
	return action, nil
}

func handleCreateRule(vault *vaultState) fiber.Handler {
	return func(c *fiber.Ctx) error {
		b, err := currentBackendOrLocked(vault)
		if err != nil {
			return err
		}
		var req ruleRequest
		if err := c.BodyParser(&req); err != nil {
			return fiber.NewError(fiber.StatusBadRequest, "invalid request body")
		}
		action, err := req.validate()
		if err != nil {
			return fiber.NewError(fiber.StatusBadRequest, err.Error())
		}

		rule := &domain.Rule{
			Name: req.Name, Priority: req.Priority, Conditions: req.Conditions, Action: action,
			IsActive: req.IsActive,
		}
		id, err := b.rulesRepo.Create(c.Context(), rule)
		if err != nil {
			return fiber.NewError(fiber.StatusInternalServerError, "creating rule failed")
		}
		rule.ID = id
		return c.Status(fiber.StatusCreated).JSON(ruleResponseFrom(rule))
	}
}

func handleUpdateRule(vault *vaultState) fiber.Handler {
	return func(c *fiber.Ctx) error {
		b, err := currentBackendOrLocked(vault)
		if err != nil {
			return err
		}
		id, err := idParam(c, "id")
		if err != nil {
			return err
		}
		var req ruleRequest
		if err := c.BodyParser(&req); err != nil {
			return fiber.NewError(fiber.StatusBadRequest, "invalid request body")
		}
		action, err := req.validate()
		if err != nil {
			return fiber.NewError(fiber.StatusBadRequest, err.Error())
		}

		rule := &domain.Rule{
			ID: id, Name: req.Name, Priority: req.Priority, Conditions: req.Conditions, Action: action,
			IsActive: req.IsActive,
		}
		if err := b.rulesRepo.Update(c.Context(), rule); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fiber.NewError(fiber.StatusNotFound, "rule not found")
			}
			return fiber.NewError(fiber.StatusInternalServerError, "updating rule failed")
		}
		return c.JSON(ruleResponseFrom(rule))
	}
}

func handleDeleteRule(vault *vaultState) fiber.Handler {
	return func(c *fiber.Ctx) error {
		b, err := currentBackendOrLocked(vault)
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
		return c.SendStatus(fiber.StatusNoContent)
	}
}
