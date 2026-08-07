package thinking

import "testing"

func TestUltraCompatibility(t *testing.T) {
	level, ok := ParseLevelSuffix("ultra")
	if !ok || level != LevelUltra {
		t.Fatalf("ParseLevelSuffix(ultra) = %q, %t", level, ok)
	}
	if budget, okBudget := ConvertLevelToBudget("ultra"); !okBudget || budget != 128000 {
		t.Fatalf("ConvertLevelToBudget(ultra) = %d, %t", budget, okBudget)
	}
	if effort, okEffort := MapToClaudeEffort("ultra", true); !okEffort || effort != "max" {
		t.Fatalf("MapToClaudeEffort(ultra) = %q, %t", effort, okEffort)
	}
}
