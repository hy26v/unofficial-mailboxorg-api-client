// ABOUTME: User-configured policy that blocks destructive CLI operations before authentication.
// ABOUTME: Supports all-destructive or resource-scoped protection; batch run needs none (it plans reads only).
package cli

import (
	"fmt"
	"strings"

	cliErrors "github.com/hy26v/unofficial-mailboxorg-api-client/internal/errors"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/operations"
)

const deleteProtectionSetting = "safety.delete_protection"

type deleteProtection struct {
	all       bool
	selectors map[string]struct{}
}

func parseDeleteProtection(values []string) (deleteProtection, *cliErrors.Error) {
	if len(values) == 0 {
		return deleteProtection{}, nil
	}

	valid := make(map[string]struct{})
	for _, op := range operations.Default.List() {
		if !operationIsDestructive(op) {
			continue
		}
		valid[strings.ToLower(op.Resource)] = struct{}{}
		valid[strings.ToLower(op.Name)] = struct{}{}
	}

	selectors := make(map[string]struct{})
	for _, rawSelector := range values {
		selector := strings.TrimSpace(rawSelector)
		if selector == "" {
			return deleteProtection{}, invalidDeleteProtection(rawSelector)
		}
		selector = strings.ToLower(selector)
		if selector == "all" {
			if len(values) != 1 {
				return deleteProtection{}, &cliErrors.Error{
					Code:    "invalid_configuration",
					Message: deleteProtectionSetting + ` cannot combine "all" with other selectors`,
				}
			}
			return deleteProtection{all: true}, nil
		}
		if _, ok := valid[selector]; !ok {
			return deleteProtection{}, invalidDeleteProtection(selector)
		}
		selectors[selector] = struct{}{}
	}

	return deleteProtection{selectors: selectors}, nil
}

func invalidDeleteProtection(selector string) *cliErrors.Error {
	return &cliErrors.Error{
		Code: "invalid_configuration",
		Message: fmt.Sprintf(
			"%s contains unknown selector %q; use [\"all\"], [], or an array of destructive resources/operations such as [\"mail\", \"folders\"]",
			deleteProtectionSetting,
			selector,
		),
	}
}

func operationIsDestructive(op operations.Operation) bool {
	return op.Destructive || op.SideEffect == operations.SideEffectDestructive
}

func (p deleteProtection) protects(op operations.Operation) bool {
	if !operationIsDestructive(op) {
		return false
	}
	if p.all {
		return true
	}
	if _, ok := p.selectors[strings.ToLower(op.Resource)]; ok {
		return true
	}
	_, ok := p.selectors[strings.ToLower(op.Name)]
	return ok
}

func (p deleteProtection) protectsResource(resource string) bool {
	if p.all {
		return true
	}
	_, ok := p.selectors[strings.ToLower(resource)]
	return ok
}

func operationForArgs(args []string) (operations.Operation, bool, int) {
	if len(args) == 0 {
		return operations.Operation{}, false, 0
	}
	if len(args) > 1 && !strings.HasPrefix(args[1], "-") {
		if op, ok := operations.Default.Get(args[0] + "." + args[1]); ok {
			return op, true, 2
		}
	}
	op, ok := operations.Default.Get(args[0])
	return op, ok, 1
}

func commandDeleteProtectionError(args, configuredSelectors []string, prepared *preparedMailMoveMany) *cliErrors.Error {
	op, ok, flagStart := operationForArgs(args)
	if !ok {
		return nil
	}

	mailMoveToTrash := commandMailMoveTargetsTrash(args, op, flagStart, prepared)
	if !operationIsDestructive(op) && !mailMoveToTrash {
		return nil
	}
	if commandRequestsDryRun(args, op, flagStart) {
		return nil
	}

	protection, err := parseDeleteProtection(configuredSelectors)
	if err != nil {
		return err
	}
	if mailMoveToTrash {
		if protection.protectsResource("mail") {
			return protectedDeleteError(op.Name)
		}
		return nil
	}
	if !protection.protects(op) {
		return nil
	}
	return protectedDeleteError(op.Name)
}

// commandMailMoveTargetsTrash recognises the deletion-equivalent form of mail.move
// without authenticating or resolving anything through the provider. The OX default-folder
// identifier is stable for the authenticated account, and matching it locally keeps the
// delete-protection decision ahead of every network path.
func commandMailMoveTargetsTrash(args []string, op operations.Operation, flagStart int, prepared *preparedMailMoveMany) bool {
	if op.Name == "mail.move" {
		destination, ok := commandStringFlagValue(args, op, flagStart, "to")
		return ok && isMailTrashFolder(destination)
	}
	if op.Name == "mail.draft" {
		// --replace-id moves the replaced draft to Trash.
		_, ok := commandStringFlagValue(args, op, flagStart, "replace-id")
		return ok
	}
	if op.Name == "mail.move-many" {
		if prepared == nil {
			return false
		}
		for _, move := range prepared.moves {
			if isMailTrashFolder(move.To) {
				return true
			}
		}
	}
	return false
}

func isMailTrashFolder(folder string) bool {
	return strings.EqualFold(strings.Trim(strings.TrimSpace(folder), "/"), mailTrashDefaultFolder)
}

func protectedDeleteError(operation string) *cliErrors.Error {
	return &cliErrors.Error{
		Code: "delete_protected",
		Message: fmt.Sprintf(
			"%s is blocked by %s in the mbx config file; --dry-run is still allowed",
			operation,
			deleteProtectionSetting,
		),
	}
}

func normalizeBatchResource(resource string) string {
	return strings.Trim(strings.ToLower(strings.TrimSpace(resource)), "/")
}
