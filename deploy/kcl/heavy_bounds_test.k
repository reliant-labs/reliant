# Exercise the generated schema defaults and the actual environment mapper.
import config_gen

_default = config_gen.daemonConfigEnvMap(config_gen.DaemonConfig {}, [])
assert str(_default["RELIANT_HEAVY_CPU_WEIGHT"]) == "0"
assert str(_default["RELIANT_HEAVY_MEMORY_HIGH"]) == ""

_configured = config_gen.daemonConfigEnvMap(config_gen.DaemonConfig {
    heavy_cpu_weight = 17
    heavy_memory_high = "16G"
}, [])
assert str(_configured["RELIANT_HEAVY_CPU_WEIGHT"]) == "17"
assert str(_configured["RELIANT_HEAVY_MEMORY_HIGH"]) == "16G"

_off = config_gen.daemonConfigEnvMap(config_gen.DaemonConfig {
    heavy_cpu_weight = 100
    heavy_memory_high = "off"
}, [])
assert str(_off["RELIANT_HEAVY_CPU_WEIGHT"]) == "100"
assert str(_off["RELIANT_HEAVY_MEMORY_HIGH"]) == "off"

passed = True
