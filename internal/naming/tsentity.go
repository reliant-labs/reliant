package naming

// EntityTypeLocalSuffix is what a generated frontend file appends to an
// entity's message name to bind its type: `import type { Link as LinkEntity }`.
//
// An entity is named by the user, and a generated page also imports names the
// user never sees: `Link` from next/link or @tanstack/react-router, `Image`,
// `Head`, `Script`, `Form`, React hooks, the component library's `Button` and
// `Input`. A page that bound the entity type under its own name declared that
// identifier twice the moment an entity shared one (`import type { Link }`
// beside `import Link from "next/link"`), and the frontend no longer
// type-checked. Binding it under a suffixed local name makes the collision
// impossible whatever the entity is called, without forge keeping a list of
// reserved names that would be wrong the day a template imports something new.
//
// The upgrade codemod `entity-type-alias` (internal/codemod) rewrites pages
// scaffolded before the alias to the same name, so it reads this too.
const EntityTypeLocalSuffix = "Entity"

// EntityTypeLocal is the local name a generated frontend file binds entity's
// message type to: "Link" → "LinkEntity".
func EntityTypeLocal(entity string) string { return entity + EntityTypeLocalSuffix }

// EnumTypeLocalSuffix is what a generated page appends to a protobuf-es enum's
// exported name to bind it: `import { Link as LinkEnum }`. An enum is named by
// the user just as an entity is, so an enum `Link` beside next/link's `Link`
// declared the identifier twice in the same way (see EntityTypeLocalSuffix).
// Every reference the page makes to the enum — a member (`LinkEnum.PRIMARY`),
// a StatusBadge's enumType, a zod pipe — goes through the local name.
const EnumTypeLocalSuffix = "Enum"

// EnumTypeLocal is the local name a generated page binds the protobuf-es enum
// exported as tsName to: "Link" → "LinkEnum", "Brand_Status" → "Brand_StatusEnum".
func EnumTypeLocal(tsName string) string { return tsName + EnumTypeLocalSuffix }
