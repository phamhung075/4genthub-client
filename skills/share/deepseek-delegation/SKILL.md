---
name: deepseek-delegation
description: Delegate bounded bulk work to the seat's deepseek MCP server (deepseek_agent and its siblings) - what to offload, what never to, and how the job's directory is decided.
---
# Delegating to the deepseek server

A seat started with `DEEPSEEK_MCP_SERVER` in its environment (the state root's `.env`, or the seat's spec)
carries a `deepseek` MCP server beside `agenthub_http`, whichever runtime hosts it: omp reads it from the
seat's `mcp.json`, claude-code is given that file with `--mcp-config`, and agy reads its own guarded entry.
The tools are `deepseek_agent` (start a job), `deepseek_update_session` (steer or cancel one that is still
running), `deepseek_list_sessions`, and `deepseek_mcp_servers`. With no such variable there is no entry at
all - an absent tool is the configuration, not a broken call, so do not hand-work around it and do not
invent a path.

## What to offload

- bounded bulk work: searching many files, reading long logs or a large diff, mechanical edits across files;
- a second pair of eyes on a large diff - its answer is a lead, not a verdict;
- give each job its files, its goal and the commands to run, and end with "Report only what you actually
  ran and observed". Give parallel workers disjoint lists, never the same files.

## What never to offload

- a decision, a commit, or a message to another seat;
- anything touching secrets, a `.env`, or another room's tree;
- work whose result you cannot check yourself afterwards.

## How the job's directory is decided

The seat's `mcp.json` passes `DEEPSEEK_MCP_DEFAULT_CWD` = the seat's own first scope directory, set by the
runtime at every seat start. Pass `cwd` explicitly when a job belongs to a subdirectory, and never assume
the directory the daemon was started in: one daemon starts every seat of every room.

## Its writes are not trusted

The server runs jobs under a git write guard, so commits and pushes from a job are refused. Read the diff
it produced and rerun the checks yourself before you count the work as done. `DEEPSEEK_MCP_READ_ONLY=1`
pins an investigation job to the read-only file policy instead of asking it not to touch files.
