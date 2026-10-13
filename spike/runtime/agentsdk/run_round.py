"""Candidate B of ADR-006: one agent's turn in one round on the Claude Agent SDK.

The sandbox passes a session token as ANTHROPIC_API_KEY and the LLM gateway as
ANTHROPIC_BASE_URL. Every mode keeps tools=[], the gateway as the only MCP
server, dontAsk, the end_turn hook and the agent's own system prompt. --hardening
then chooses what else is switched off:

- full (the default): setting_sources=[], strict_mcp_config, a private
  CLAUDE_CONFIG_DIR and the environment switches below;
- minimal: only setting_sources=[] and strict_mcp_config;
- none: nothing more, as a control that shows what hardening prevents.
"""

import argparse
import json
import os
import sys
import tempfile

import anyio
from claude_agent_sdk import ClaudeAgentOptions, ClaudeSDKClient, HookMatcher, ResultMessage

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
    if args.hardening in ("full", "minimal"):
        opts.setting_sources = []
        opts.strict_mcp_config = True
    if args.hardening == "full":
        opts.env = dict(HARDENING_ENV, CLAUDE_CONFIG_DIR=tempfile.mkdtemp(prefix="claude-config-"))
    return opts


async def run(args):
    """Plays the turn until the round's limit, which counts from the start.

    At the limit the turn is interrupted, then the session closes. Both run
    under anyio cancel scopes: the SDK's close() is shielded only against anyio
    cancellation, and a raw asyncio one (asyncio.wait_for) leaves the CLI
    running after the limit.
    """
    deadline = anyio.current_time() + args.round_seconds
    with open(args.system_file, encoding="utf-8") as f:
        system = f.read()
    with open(args.prompt_file, encoding="utf-8") as f:
        prompt = f.read()
    result = {"outcome": "no_result"}
    async with ClaudeSDKClient(options=options(args, system)) as client:
        with anyio.move_on_at(deadline) as scope:
            await client.query(prompt)
            async for message in client.receive_response():
                if isinstance(message, ResultMessage):
                    result = {"outcome": message.subtype, "turns": message.num_turns, "stop_reason": message.stop_reason}
        if scope.cancelled_caught:
            result = {"outcome": "deadline"}
            with anyio.move_on_after(2):
                await client.interrupt()
    return result


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--mcp-url", default="http://mcpgw:8090/mcp")
    parser.add_argument("--model", default="claude-haiku-5-5")
    parser.add_argument("--system-file", default="/in/system.md")
    parser.add_argument("--prompt-file", default="/in/prompt.md")
    parser.add_argument("--round-seconds", type=float, default=120)
    parser.add_argument("--max-steps", type=int, default=30)
    parser.add_argument("--hardening", choices=["full", "minimal", "none"], default="full",
                        help="what is switched off beyond the common options (see the module docstring)")
    args = parser.parse_args()
    result = anyio.run(run, args)
    print(json.dumps(result))
    sys.stdout.flush()


if __name__ == "__main__":
    main()
