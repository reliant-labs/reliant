package activation

import "testing"

func TestClassifyTurn_MetaCatalogQueryIsNotAutoEligible(t *testing.T) {
	classification := ClassifyTurn(TurnInput{LatestUserMessage: "what skills do you have", ActivationMode: "auto"})
	if classification.Intent != IntentMetaCatalog {
		t.Fatalf("expected meta catalog intent, got %s", classification.Intent)
	}
	if classification.AutoEligible {
		t.Fatalf("expected meta catalog query to be ineligible for auto activation")
	}
}

func TestClassifyTurn_ExplicitInvocation(t *testing.T) {
	classification := ClassifyTurn(TurnInput{LatestUserMessage: "/frontend-design", ActivationMode: "auto"})
	if classification.Intent != IntentExplicitSkill {
		t.Fatalf("expected explicit skill intent, got %s", classification.Intent)
	}
	if classification.ExplicitName != "frontend-design" {
		t.Fatalf("expected explicit name frontend-design, got %q", classification.ExplicitName)
	}
}

func TestClassifyTurn_DefaultsExecutionForConcreteRequest(t *testing.T) {
	classification := ClassifyTurn(TurnInput{LatestUserMessage: "redesign my homepage hero", ActivationMode: "auto"})
	if classification.Intent != IntentExecution {
		t.Fatalf("expected execution intent, got %s", classification.Intent)
	}
	if !classification.AutoEligible {
		t.Fatalf("expected execution request to remain auto eligible")
	}
}
