use serde::{Deserialize, Serialize};
use std::collections::HashMap;
use std::fs;
use std::path::{Path, PathBuf};

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct SkillMetadata {
    pub name: String,
    pub description: String,
    #[serde(default)]
    pub tools: Vec<String>,
    #[serde(flatten)]
    pub extra: HashMap<String, serde_yaml::Value>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ResolvedSkill {
    pub name: String,
    pub path: PathBuf,
    pub content: String,
    pub metadata: Option<SkillMetadata>,
    pub tier: SkillTier,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum SkillTier {
    Shared,
    Room,
    Seat,
    Client,
}

pub struct SkillResolver {
    skills_root: PathBuf,
}

impl SkillResolver {
    pub fn new(root: impl Into<PathBuf>) -> Self {
        Self {
            skills_root: root.into(),
        }
    }

    /// Parse frontmatter and markdown body from SKILL.md.
    pub fn parse_skill_file(path: &Path) -> Result<(Option<SkillMetadata>, String), String> {
        let content = fs::read_to_string(path).map_err(|e| format!("read {}: {e}", path.display()))?;
        if content.starts_with("---") {
            if let Some(end_idx) = content[3..].find("---") {
                let yaml_str = &content[3..3 + end_idx];
                let body = content[3 + end_idx + 3..].trim().to_string();
                let meta: Option<SkillMetadata> = serde_yaml::from_str(yaml_str)
                    .map_err(|e| format!("{}: frontmatter does not parse: {e}", path.display()))?;
                return Ok((meta, body));
            }
        }
        Ok((None, content))
    }

    /// Resolve all skills for a specific seat in a room.
    pub fn resolve_for_seat(&self, room: &str, seat: &str) -> Result<Vec<ResolvedSkill>, String> {
        let mut resolved = Vec::new();

        // 1. Shared skills (skills/share/*)
        let share_dir = self.skills_root.join("share");
        self.collect_skills_in_dir(&share_dir, SkillTier::Shared, &mut resolved)?;

        // 2. Room skills (skills/rooms/<room>/*)
        let room_dir = self.skills_root.join("rooms").join(room);
        if room_dir.is_dir() {
            if let Ok(entries) = fs::read_dir(&room_dir) {
                for entry in entries.flatten() {
                    let path = entry.path();
                    if path.is_dir() {
                        let skill_file = path.join("SKILL.md");
                        if skill_file.is_file() {
                            // Directory has SKILL.md -> room level skill
                            let name = entry.file_name().to_string_lossy().to_string();
                            let (meta, body) = Self::parse_skill_file(&skill_file)?;
                            resolved.push(ResolvedSkill {
                                name,
                                path: skill_file,
                                content: body,
                                metadata: meta,
                                tier: SkillTier::Room,
                            });
                        } else if entry.file_name() == seat {
                            // 3. Seat directory matching this seat (skills/rooms/<room>/<seat>/*)
                            self.collect_skills_in_dir(&path, SkillTier::Seat, &mut resolved)?;
                        }
                    }
                }
            }
        }

        Ok(resolved)
    }

    fn collect_skills_in_dir(
        &self,
        dir: &Path,
        tier: SkillTier,
        out: &mut Vec<ResolvedSkill>,
    ) -> Result<(), String> {
        if !dir.is_dir() {
            return Ok(());
        }
        let Ok(entries) = fs::read_dir(dir) else { return Ok(()) };
        for entry in entries.flatten() {
            let path = entry.path();
            if path.is_dir() {
                let skill_file = path.join("SKILL.md");
                if skill_file.is_file() {
                    let name = entry.file_name().to_string_lossy().to_string();
                    let (meta, body) = Self::parse_skill_file(&skill_file)?;
                    out.push(ResolvedSkill {
                        name,
                        path: skill_file,
                        content: body,
                        metadata: meta,
                        tier,
                    });
                }
            }
        }
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::fs::File;
    use std::io::Write;

    #[test]
    fn test_parse_frontmatter() {
        let temp_dir = std::env::temp_dir().join("skill_test_dir");
        let _ = fs::create_dir_all(&temp_dir);
        let skill_file = temp_dir.join("SKILL.md");
        let mut f = File::create(&skill_file).unwrap();
        writeln!(
            f,
            "---\nname: test-skill\ndescription: A test skill\ntools: [read, grep]\n---\n# Instructions\nFollow instructions."
        ).unwrap();

        let (meta, body) = SkillResolver::parse_skill_file(&skill_file).unwrap();
        assert!(meta.is_some());
        let m = meta.unwrap();
        assert_eq!(m.name, "test-skill");
        assert_eq!(m.tools, vec!["read", "grep"]);
        assert!(body.contains("# Instructions"));

        let _ = fs::remove_dir_all(&temp_dir);
    }

    /// The seat tier carries the seat's own role: one skill per seat, resolved for that seat alone, read
    /// from the room's seat directory. The seat-type's role text, its rules and its output format used to
    /// reach a seat as a rendered `guidance/role.md`; the render no longer carries them, so this is the
    /// only delivery, and a missing 4genthub-min seat directory has to fail here rather than be found by a
    /// seat that has no contract. Landmarks beside the assertion: the share tier still resolves, and every
    /// OTHER seat's role skill stays out of this seat's set - a resolver handed one directory could show an
    /// absence for free.
    #[test]
    fn a_seat_resolves_its_own_role_skill_and_no_other_seats() {
        let root = Path::new(env!("CARGO_MANIFEST_DIR")).join("..").join("..").join("skills");
        let resolver = SkillResolver::new(root);
        // (seat, a landmark from the type's role text, a landmark from its rules)
        // (seat, a landmark from the type's role text, a landmark from its rules, the version its text
        // was ported at) - the reviewer's copy moved to 1.4.19 with the decision check, the other four
        // are still the 1.4.12 copy.
        let cases = [
            ("lead", "You are the lead seat", "One task per assignment", "1.4.12"),
            ("architect", "You are the architect seat", "Options and trade-offs", "1.4.12"),
            ("go-dev", "You own implementation", "Smallest safe change", "1.4.12"),
            ("fe-dev", "You own implementation", "Smallest safe change", "1.4.12"),
            ("reviewer", "You are the reviewer seat", "Review against the task", "1.4.19"),
        ];
        let seats: Vec<&str> = cases.iter().map(|(seat, _, _, _)| *seat).collect();

        for (seat, role_landmark, rule_landmark, seat_type_version) in cases {
            let resolved = resolver
                .resolve_for_seat("4genthub-min", seat)
                .expect("the skill tree resolves for this seat");
            assert!(
                resolved.iter().any(|s| s.tier == SkillTier::Shared),
                "{seat}: the share tier resolved nothing, so this case is reading a root it cannot see"
            );

            let want_name = format!("{seat}-role");
            let role = resolved
                .iter()
                .find(|s| s.name == want_name)
                .unwrap_or_else(|| {
                    panic!(
                        "{seat}: no skill named {want_name}; the seat tier holds {:?}",
                        resolved
                            .iter()
                            .filter(|s| s.tier == SkillTier::Seat)
                            .map(|s| s.name.as_str())
                            .collect::<Vec<_>>()
                    )
                });
            assert_eq!(role.tier, SkillTier::Seat, "{seat}: {want_name} resolved outside the seat tier");
            assert!(
                role.path.to_string_lossy().ends_with(&format!(
                    "rooms/4genthub-min/{seat}/{want_name}/SKILL.md"
                )),
                "{seat}: {want_name} came from {}",
                role.path.display()
            );
            let meta = role
                .metadata
                .as_ref()
                .unwrap_or_else(|| panic!("{seat}: {want_name} carries no frontmatter"));
            assert_eq!(meta.name, want_name);
            assert!(!meta.description.trim().is_empty(), "{seat}: {want_name} has no description");
            assert_eq!(
                meta.extra.get("seat-type-version").and_then(|v| v.as_str()),
                Some(seat_type_version),
                "{seat}: {want_name} does not name the seat-type-version its text was ported at"
            );
            assert!(
                role.content.contains(role_landmark),
                "{seat}: {want_name} does not carry its type's role text ({role_landmark:?})"
            );
            assert!(
                role.content.contains(rule_landmark),
                "{seat}: {want_name} does not carry its type's rules ({rule_landmark:?})"
            );
            assert!(
                role.content.contains("## Output format"),
                "{seat}: {want_name} does not carry its type's output format"
            );

            for other in &seats {
                if other != &seat {
                    assert!(
                        !resolved.iter().any(|s| s.name == format!("{other}-role")),
                        "{seat}: the seat tier handed this seat {other}'s role"
                    );
                }
            }
        }

        // Landmark on the seat tier that predates this one: fe-dev's own skills stay theirs.
        let fe = resolver
            .resolve_for_seat("4genthub-min", "fe-dev")
            .expect("the skill tree resolves for fe-dev");
        assert!(fe.iter().any(|s| s.name == "taste-skill" && s.tier == SkillTier::Seat));
        let go = resolver
            .resolve_for_seat("4genthub-min", "go-dev")
            .expect("the skill tree resolves for go-dev");
        assert!(!go.iter().any(|s| s.name == "taste-skill"));
    }

    /// A frontmatter that is PRESENT but does not parse is a NAMED failure: the resolver returns the skill
    /// file and the yaml problem instead of resolving the skill with `metadata: None`. This is the defect a
    /// reviewer's gate measured - an unquoted `: ` inside one seat-role description made serde_yaml refuse
    /// the block, the skill resolved with no name, description or seat-type-version, and the only thing that
    /// failed was a test's own "carries no frontmatter" assertion.
    #[test]
    fn a_frontmatter_that_does_not_parse_is_named_and_never_dropped() {
        let root = std::env::temp_dir().join(format!("skill_bad_fm_{}", std::process::id()));
        let skill_file = root.join("rooms").join("demo").join("bad").join("SKILL.md");
        fs::create_dir_all(skill_file.parent().unwrap()).unwrap();
        fs::write(
            &skill_file,
            "---\nname: bad\ndescription: you own this contract: own coordination\n---\nbody\n",
        )
        .unwrap();

        let err = SkillResolver::new(&root)
            .resolve_for_seat("demo", "nobody")
            .expect_err("a malformed frontmatter must refuse resolution, not resolve the skill");
        assert!(err.contains("SKILL.md"), "the refusal names the file: {err}");
        assert!(
            err.contains("frontmatter does not parse"),
            "the refusal names the problem: {err}"
        );
        let _ = fs::remove_dir_all(&root);
    }

    /// A file with NO frontmatter stays legal, and resolves with no metadata - the half that must not turn
    /// into an error. (The seat-role case above asserts the other half against the real tree: a valid block
    /// still parses and carries its name, description and seat-type-version.)
    #[test]
    fn a_file_without_frontmatter_still_resolves() {
        let root = std::env::temp_dir().join(format!("skill_no_fm_{}", std::process::id()));
        let skill_dir = root.join("share").join("plain");
        fs::create_dir_all(&skill_dir).unwrap();
        fs::write(skill_dir.join("SKILL.md"), "# Plain skill\nno frontmatter here\n").unwrap();

        let resolved = SkillResolver::new(&root)
            .resolve_for_seat("demo", "nobody")
            .expect("no frontmatter is legal");
        assert_eq!(resolved.len(), 1);
        assert!(resolved[0].metadata.is_none());
        assert!(resolved[0].content.contains("no frontmatter here"));
        let _ = fs::remove_dir_all(&root);
    }
}
