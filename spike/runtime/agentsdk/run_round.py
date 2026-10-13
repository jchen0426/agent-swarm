"""Candidate B of ADR-006: one agent's turn in one round on the Claude Agent SDK.

The sandbox passes a session token as ANTHROPIC_API_KEY and the LLM gateway as
ANTHROPIC_BASE_URL. Hardened mode switches off everything documented to load
host or workspace content; --defaults keeps the SDK's defaults, as a control
that shows what the hardening prevents.
"""

import argparse
import asyncio
import json
import os
import sys
import tempfile

from claude_agent_sdk import ClaudeAgentOptions, HookMatcher, ResultMessage, query

END_TOOL = "mcp__gw__end_turn"

HARDENING_ENV = {
    "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
    "DISABLE_AUTOUPDATER": "1",
    "CLAUDE_CODE_DISABLE_AUTO_MEMORY": "1",
    "CLAUDE_CODE_DISABLE_CLAUDE_MDS": "1",
    "CLAUDE_CODE_DISABLE_ATTACHMENTS": "1",
    "CLAUDE_CODE_DISABLE_GIT_INSTRUCTIONS": "1",
    "CLAUDE_CODE_ATTRIBUTION_HEADER": "0",
    "CLAUDE_CODE_SKIP_PROMPT_HISTORY": "1",
    "CLAUDE_CODE_DISABLE_TERMINAL_TITLE": "1",
    "CLAUDE_AGENT_SDK_DISABLE_BUILTIN_AGENTS": "1",
    "DISABLE_AUTO_COMPACT": "1",
    "ENABLE_CLAUDEAI_MCP_SERVERS": "false",
}


async def stop_after_end_turn(_input, _tool_use_id, _context):
    """Ends the session as soon as the agent's end_turn call has run."""
    return {"continue_": False, "stopReason": "end_turn"}


def options(args, system):
    stop = [HookMatcher(matcher=END_TOOL, hooks=[stop_after_end_turn])]
    opts = ClaudeAgentOptions(
        system_prompt=system,
        tools=[],
        allowed_tools=["mcp__gw__*"],
        permission_mode="dontAsk",
        mcp_servers={"gw": {"type": "http", "url": args.mcp_url}},
        max_turns=args.max_steps,
        model=args.model,
        cwd=os.getcwd(),
        hooks={"PostToolUse": stop, "PostToolUseFailure": stop},
    )
    if not args.defaults:
        opts.setting_sources = []
        opts.strict_mcp_config = True
        opts.env = dict(HARDENING_ENV, CLAUDE_CONFIG_DIR=tempfile.mkdtemp(prefix="claude-config-"))
    return opts


async def run(args):
    with open(args.system_file, encoding="utf-8") as f:
        system = f.read()
    with open(args.prompt_file, encoding="utf-8") as f:
        prompt = f.read()
    result = {"outcome": "no_result"}
    async for message in query(prompt=prompt, options=options(args, system)):
        if isinstance(message, ResultMessage):
            result = {"outcome": message.subtype, "turns": message.num_turns, "stop_reason": message.stop_reason}
    return result


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--mcp-url", default="http://mcpgw:8090/mcp")
    parser.add_argument("--model", default="claude-sonnet-5-5")
    parser.add_argument("--system-file", default="/in/system.md")
    parser.add_argument("--prompt-file", default="/in/prompt.md")
    parser.add_argument("--round-seconds", type=float, default=120)
    parser.add_argument("--max-steps", type=int, default=30)
    parser.add_argument("--defaults", action="store_true", help="keep the SDK's defaults (control run)")
    args = parser.parse_args()
    try:
        result = asyncio.run(asyncio.wait_for(run(args), timeout=args.round_seconds))
    except TimeoutError:
        result = {"outcome": "deadline"}
    print(json.dumps(result))
    sys.stdout.flush()


if __name__ == "__main__":
    main()
