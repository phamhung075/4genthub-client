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
                let meta: Option<SkillMetadata> = serde_yaml::from_str(yaml_str).ok();
                return Ok((meta, body));
            }
        }
        Ok((None, content))
    }

    /// Resolve all skills for a specific seat in a room.
    pub fn resolve_for_seat(&self, room: &str, seat: &str) -> Vec<ResolvedSkill> {
        let mut resolved = Vec::new();

        // 1. Shared skills (skills/share/*)
        let share_dir = self.skills_root.join("share");
        self.collect_skills_in_dir(&share_dir, SkillTier::Shared, &mut resolved);

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
                            if let Ok((meta, body)) = Self::parse_skill_file(&skill_file) {
                                resolved.push(ResolvedSkill {
                                    name,
                                    path: skill_file,
                                    content: body,
                                    metadata: meta,
                                    tier: SkillTier::Room,
                                });
                            }
                        } else if entry.file_name() == seat {
                            // 3. Seat directory matching this seat (skills/rooms/<room>/<seat>/*)
                            self.collect_skills_in_dir(&path, SkillTier::Seat, &mut resolved);
                        }
                    }
                }
            }
        }

        resolved
    }

    fn collect_skills_in_dir(&self, dir: &Path, tier: SkillTier, out: &mut Vec<ResolvedSkill>) {
        if !dir.is_dir() {
            return;
        }
        let Ok(entries) = fs::read_dir(dir) else { return };
        for entry in entries.flatten() {
            let path = entry.path();
            if path.is_dir() {
                let skill_file = path.join("SKILL.md");
                if skill_file.is_file() {
                    let name = entry.file_name().to_string_lossy().to_string();
                    if let Ok((meta, body)) = Self::parse_skill_file(&skill_file) {
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
        }
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
}
