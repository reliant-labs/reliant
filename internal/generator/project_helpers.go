package generator

import (
	"sync"

	"github.com/reliant-labs/forge/internal/templates"
)

var (
	templateEngineOnce sync.Once
	templateEngineInst *templates.TemplateEngine
	templateEngineErr  error
)

func getTemplateEngine() (*templates.TemplateEngine, error) {
	templateEngineOnce.Do(func() {
		templateEngineInst, templateEngineErr = templates.NewTemplateEngine()
	})
	return templateEngineInst, templateEngineErr
}

// renderServiceTemplate renders a service template from the embedded FS.
func renderServiceTemplate(name string, data interface{}) ([]byte, error) {
	engine, err := getTemplateEngine()
	if err != nil {
		return nil, err
	}
	result, err := engine.RenderTemplate(name, data)
	if err != nil {
		return nil, err
	}
	return []byte(result), nil
}
