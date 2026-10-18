# The development-team skill stops citing a reference that does not exist

`skills/share/development-team/SKILL.md` told a seat that "the complete lookup and precedence rule is
`docs/reference/product-journey-sdlc.md#resolve-the-selected-path` (installed:
`<rig install>/reference/product-journey-sdlc.md#resolve-the-selected-path`)". That file exists in no tree:
not in this repository at the pin or at its head, not in the hub repository, and not in the rig's own
configuration directory. A seat following the instruction would spend a lookup and find nothing, and the
skill's own chain - `project.yaml -> mission.yaml -> active slice.yaml -> selected component or wave map ->
addressed context` - already states the order the missing document was cited to explain.

- The dead pointer and its "(installed: ...)" fallback are replaced by the rule they were cited to carry:
  the order named above is the lookup and precedence rule. Nothing else in the section changes.

The pointer was found by the doc citation sweep of 2026-10-18 (hub board item `79813f48`): 731 citations
swept across 46 `ai_docs` files and 68 skills at the pin, 198 dead, 164 in repo scope, 6 presented as current
- this is the client tree's one.

No code and no test changed: documentation.
