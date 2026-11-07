# One DocType per file. Format: docs/blueprint/architecture.md in the Forgebase checkout.
# A DocType name is global: it starts with the app name, so apps cannot clash.
name = "{{name}}_note"
label = "{{title}} Note"

[[fields]]
name = "title"
type = "data"
required = true

[[fields]]
name = "body"
type = "text"

# Who may do what. A DocType without rules is closed to everyone except `Administrator`.
[[permissions]]
role = "{{title}} User"
read = true
write = true
create = true
delete = true
