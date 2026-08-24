package agent

import "fmt"

// ComposeProgramFlags folds the optional Claude model, permission-mode, and
// effort overrides into program, returning the augmented command. Each is
// applied only when non-empty and the program resolves to claude — the sole
// agent whose --model / --permission-mode / --effort flags these compose — so
// any other agent's program passes through untouched, which is what lets a
// mixed variant batch carry the pins on its claude members alone.
//
// Values are re-validated here as the shared backstop for both callers. The
// create form filters its model field to ValidModelName's charset (see
// ui/overlay/modelField.go) and offers closed chip sets for mode and effort,
// so an invalid value arriving from it means UI/enum drift; `atrium new` takes
// the same three values from flags, where a typo is the ordinary case. Either
// way the miss is caught here, before a dead launch. The mode check sees the
// model-augmented program, matching the form's submit order; since --model
// leaves the base command claude, Resolve is unaffected. Note the claude CLI
// soft-validates --effort (an unknown value is warned-and-ignored, not
// rejected like --permission-mode), so ValidEffort is a stricter gate than the
// launch itself would be.
func ComposeProgramFlags(program, model, mode, effort string) (string, error) {
	if model != "" && Resolve(program).Key == KeyClaude {
		if !ValidModelName(model) {
			return "", fmt.Errorf("invalid model name %q (letters, digits, . _ : / - only)", model)
		}
		program = WithModelFlag(program, model)
	}
	if mode != "" && Resolve(program).Key == KeyClaude {
		if !ValidPermissionMode(mode) {
			return "", fmt.Errorf("invalid permission mode %q", mode)
		}
		program = WithPermissionModeFlag(program, mode)
	}
	if effort != "" && Resolve(program).Key == KeyClaude {
		if !ValidEffort(effort) {
			return "", fmt.Errorf("invalid effort level %q", effort)
		}
		program = WithEffortFlag(program, effort)
	}
	return program, nil
}
