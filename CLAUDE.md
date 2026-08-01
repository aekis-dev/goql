# Project Development Workflow

## Interactive Design Process

### Before Any Implementation
- When starting a new feature or component, ASK questions before making assumptions[(1)](https://code.claude.com/docs/en/memory#claude-md-files)
- Never infer design decisions from code alone—always confirm with me first[(1)](https://code.claude.com/docs/en/memory#claude-md-files)
- Read @design.md first to understand existing decisions[(1)](https://code.claude.com/docs/en/memory#claude-md-files)
- Propose options and ask which approach I prefer[(1)](https://code.claude.com/docs/en/memory#claude-md-files)

### Required Questions Before Coding
When encountering new work, ask about:
- What is the intended user experience or behavior?
- Which libraries/tools should be used and why?
- How does this integrate with existing components?
- What are the performance/security requirements?
- Are there existing patterns I should follow?

### File Reading Strategy (Token Efficiency)
- NEVER read files unless you need specific information from them[(2)](https://code.claude.com/docs/en/best-practices#configure-your-environment)
- Use grep, find, or ls to locate relevant files before reading[(2)](https://code.claude.com/docs/en/best-practices#configure-your-environment)
- Read only the minimal sections needed[(2)](https://code.claude.com/docs/en/best-practices#configure-your-environment)
- Ask me which files to examine rather than reading everything[(2)](https://code.claude.com/docs/en/best-practices#configure-your-environment)

### Investigate Before Reading
- Use file system tools to understand project structure first[(2)](https://code.claude.com/docs/en/best-practices#configure-your-environment)
- Check file sizes before reading (avoid loading large files unnecessarily)[(2)](https://code.claude.com/docs/en/best-practices#configure-your-environment)
- Use grep to search for specific patterns across files[(2)](https://code.claude.com/docs/en/best-practices#configure-your-environment)
- Only read full files when absolutely necessary for the current task[(2)](https://code.claude.com/docs/en/best-practices#configure-your-environment)

## Design Documentation

### Maintain design.md
- Help me build and maintain design.md with all architectural decisions[(1)](https://code.claude.com/docs/en/memory#claude-md-files)
- Document library choices and their usage patterns[(1)](https://code.claude.com/docs/en/memory#claude-md-files)
- Record component hierarchy and data flow[(1)](https://code.claude.com/docs/en/memory#claude-md-files)
- Track API contracts and interface definitions[(1)](https://code.claude.com/docs/en/memory#claude-md-files)
- Note any project-specific conventions or patterns[(1)](https://code.claude.com/docs/en/memory#claude-md-files)

### Design Decision Format
When we make a design decision, add it to design.md using:
Decision Title]

Decision: [What we decided]
Rationale: [Why we chose this]
Alternatives considered: [What we rejected and why]
Implementation notes: [Key technical details]


## Step-by-Step Implementation

### Design First, Code Second
- When I say "let's design X", start by asking clarifying questions[(1)](https://code.claude.com/docs/en/memory#claude-md-files)
- Create a proposal showing interfaces/signatures only[(2)](https://code.claude.com/docs/en/best-practices#configure-your-environment)
- Wait for my approval before generating full implementations[(2)](https://code.claude.com/docs/en/best-practices#configure-your-environment)
- Reference design.md for context on previous decisions[(1)](https://code.claude.com/docs/en/memory#claude-md-files)

### Incremental Progress
- Focus on one component, struct, or function at a time[(3)](https://platform.claude.com/docs/en/build-with-claude/prompt-engineering/claude-prompting-best-practices#agentic-systems)
- Complete one part fully before moving to the next[(3)](https://platform.claude.com/docs/en/build-with-claude/prompt-engineering/claude-prompting-best-practices#agentic-systems)
- Make steady advances on a few things at a time[(3)](https://platform.claude.com/docs/en/build-with-claude/prompt-engineering/claude-prompting-best-practices#agentic-systems)
- Track progress in design.md as we go[(1)](https://code.claude.com/docs/en/memory#claude-md-files)

### Code Generation Rules
- Generate code only after we agree on the design[(2)](https://code.claude.com/docs/en/best-practices#configure-your-environment)
- Show the design/interface before implementation[(2)](https://code.claude.com/docs/en/best-practices#configure-your-environment)
- Keep track of agreed-upon decisions in design.md[(2)](https://code.claude.com/docs/en/best-practices#configure-your-environment)
- Libraries and dependencies must be discussed before use[(2)](https://code.claude.com/docs/en/best-practices#configure-your-environment)

## Response Conciseness

### Be Direct and Minimal
- Provide only what I asked for—no extra explanations unless requested[(2)](https://code.claude.com/docs/en/best-practices#configure-your-environment)
- Show design proposals in minimal format (interfaces/signatures only)[(2)](https://code.claude.com/docs/en/best-practices#configure-your-environment)
- Don't generate full implementations until I approve the design[(2)](https://code.claude.com/docs/en/best-practices#configure-your-environment)
- Keep responses focused on the immediate question[(2)](https://code.claude.com/docs/en/best-practices#configure-your-environment)

### Avoid Over-Engineering
- Don't add features, refactor code, or make improvements beyond what was asked[(3)](https://platform.claude.com/docs/en/build-with-claude/prompt-engineering/claude-prompting-best-practices#agentic-systems)
- Don't add error handling for scenarios that can't happen[(3)](https://platform.claude.com/docs/en/build-with-claude/prompt-engineering/claude-prompting-best-practices#agentic-systems)
- Don't create helpers or abstractions for one-time operations[(3)](https://platform.claude.com/docs/en/build-with-claude/prompt-engineering/claude-prompting-best-practices#agentic-systems)
- The right amount of complexity is the minimum needed for the current task[(3)](https://platform.claude.com/docs/en/build-with-claude/prompt-engineering/claude-prompting-best-practices#agentic-systems)

## Verification and Quality

### Self-Verification
- Always provide a way to verify the work (tests, commands, expected outputs)[(2)](https://code.claude.com/docs/en/best-practices#configure-your-environment)
- Run tests after implementing and fix any failures[(2)](https://code.claude.com/docs/en/best-practices#configure-your-environment)
- Show evidence rather than asserting success[(2)](https://code.claude.com/docs/en/best-practices#configure-your-environment)
- Address root causes, not symptoms[(2)](https://code.claude.com/docs/en/best-practices#configure-your-environment)

### When Uncertain
- If you don't know something, say so—don't guess or infer[(1)](https://code.claude.com/docs/en/memory#claude-md-files)
- Ask for clarification rather than making assumptions[(1)](https://code.claude.com/docs/en/memory#claude-md-files)
- Propose multiple options when there are tradeoffs[(1)](https://code.claude.com/docs/en/memory#claude-md-files)
- Reference design.md when decisions have already been made[(1)](https://code.claude.com/docs/en/memory#claude-md-files)

---

## Project Context
See @design.md for all design decisions and rationale