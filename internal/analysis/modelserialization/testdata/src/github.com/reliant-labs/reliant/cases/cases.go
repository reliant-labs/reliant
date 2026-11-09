package cases

import (
	"gopkg.in/yaml.v3"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
)

type Alias = db.StepExecution
type Defined struct{ Model core.WorkflowScenario }
type Envelope struct {
	Rows []*Alias `json:"-"`
}
type Recursive struct {
	Next  *Recursive
	Model *db.ChatStepExecution
}
type Clean struct{ Count int }

func exercise(w any, r any, s *db.StepExecution, c *core.ChatStepExecution, scenario db.WorkflowScenario) {
	yaml.Marshal(s)                             // want "step/scenario model at gopkg.in/yaml.v3.Marshal"
	yaml.Marshal(scenario)                      // want "step/scenario model at gopkg.in/yaml.v3.Marshal"
	yaml.Marshal(Defined{})                     // want "step/scenario model at gopkg.in/yaml.v3.Marshal"
	yaml.Marshal(Envelope{})                    // want "step/scenario model at gopkg.in/yaml.v3.Marshal"
	yaml.Marshal(Recursive{})                   // want "step/scenario model at gopkg.in/yaml.v3.Marshal"
	yaml.Marshal(map[string][]*Alias{})         // want "step/scenario model at gopkg.in/yaml.v3.Marshal"
	yaml.Marshal(map[*Alias]string{})           // want "step/scenario model at gopkg.in/yaml.v3.Marshal"
	yaml.Marshal([1]*Alias{})                   // want "step/scenario model at gopkg.in/yaml.v3.Marshal"
	yaml.Marshal(any(s))                        // want "step/scenario model at gopkg.in/yaml.v3.Marshal"
	yaml.Marshal(map[string]any{"step": s})     // want "step/scenario model at gopkg.in/yaml.v3.Marshal"
	yaml.Marshal(struct{ Value any }{Value: c}) // want "step/scenario model at gopkg.in/yaml.v3.Marshal"
	marshal := yaml.Marshal
	_ = marshal                          // function variables are outside the static-callee guarantee
	yaml.NewEncoder(w).Encode(s)         // want "step/scenario model at gopkg.in/yaml.v3.Encode"
	yaml.NewDecoder(r).Decode(&scenario) // want "step/scenario model at gopkg.in/yaml.v3.Decode"
	yaml.Unmarshal(nil, &scenario)       // want "step/scenario model at gopkg.in/yaml.v3.Unmarshal"

	// Positive cases: model projections are not models, JSON is compatibility
	// covered, and an unrelated type with the same name is not a core model.
	yaml.Marshal(Clean{Count: len([]*Alias{s})})
	yaml.Marshal(struct{ Text string }{Text: "safe"})
	type StepExecution struct{ Value int }
	yaml.Marshal(StepExecution{})
}

// Explicit limits, not negative proofs: there is no interprocedural or
// interface-variable dataflow. These are not reported by this analyzer.
func erased(w any, s *db.StepExecution) {
	var dynamic any = s
	yaml.NewEncoder(w).Encode(dynamic)
	wrap(s)
}
func wrap(value any) { yaml.Marshal(value) }
