// Deliberately ill-typed input: the analysis driver must reject the package,
// never report an apparently clean result using incomplete type information.
package broken

import "gopkg.in/yaml.v3"

func encode() { yaml.Marshal(doesNotExist) }
