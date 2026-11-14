// The shape `forge scaffold operator` emits: a controller whose exported
// methods are the controller-runtime reconciler surface the manager
// consumes. No contract.go, and no finding.
package widget

// Manager stands in for ctrl.Manager.
type Manager interface{}

// Controller reconciles widgets.
type Controller struct{}

// Name is the controller's name.
func (c *Controller) Name() string { return "widget" }

// SetupWithManager registers the controller.
func (c *Controller) SetupWithManager(Manager) error { return nil }
