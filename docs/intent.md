# agent-chat — Product Intent

This is the project's living north star: the utility agent-chat aims to
provide and its deliberate non-goals. It changes only when the *desire* for
the product changes — never because the implementation did. Entries state
direction, not absolutes: several name two goods and say which way to move
when they conflict, so a trade-off that serves the purpose better is welcome
even where it breaks with today's implementation. The test this document
must pass: an agent reading only this page should be able to recognize when
agent-chat is malfunctioning, and brainstorm improvements for it.

## Purpose

1. agent-chat exists to turn humans, computers, and AI agents into a
   single, more capable problem-solving machine — parallel work streams
   coordinating mid-task to solve problems larger than any participant
   could handle alone — typically applied to software development, but not
   only that.
2. agent-chat's part in that machine is the communication layer, and only
   that: getting what one participant knows to the participants who need
   it, while the work is still in flight. *How* that happens is an
   implementation question — today it is messages on a shared channel —
   and any mechanism that conveys understanding better is fair game.
   Everything the machine does with that understanding belongs to someone
   else.

## Principles

3. Coordination requires that a participant can direct something at a
   specific recipient and trust it reaches someone able to act on it in a
   timely manner — communication that silently goes nowhere is worse than
   communication that fails loudly.
4. Attention is the machine's scarcest resource — human and agent alike —
   and modest effort spent anywhere in the system (say, making a sender be
   deliberate about whose attention it takes) is a good trade for
   preserving it.
5. Humans are working parts of the machine, not spectators: when work hits
   something that needs human judgment, authority, or intent, carrying it
   to the right human beats stalling silently — and beats guessing
   autonomously.
6. The system stays cheap and simple to use so that it stays cheap and
   simple to understand, modify, and iterate on — improving agent-chat
   toward its purpose is a standing subgoal, and simplicity is what keeps
   that loop fast.

## Deliberate non-goals

7. **A human-facing interface.** Humans participate through their own
   agent — summaries flowing to them, steering flowing from them — so the
   raw channel is built for agent consumption, not human reading.
8. **A permanent record.** History exists so a participant can catch up,
   not as an archive — idle conversations are allowed to disappear.
9. **Adversarial identity.** Peers are trusted collaborators inside one
   security perimeter, and identity needs only to be good enough that
   communication lands on the right desk — not proof against deliberate
   impersonation.
10. **Work orchestration.** agent-chat carries what participants know; it
    never becomes the thing that decides what work happens, who does it,
    or whether it's finished. How work gets broken down, assigned, and
    tracked is a convention layered on top by the participants themselves.
