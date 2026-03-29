--------------------------- MODULE AMTPInvariants ---------------------------
(* AMTP Protocol Invariants — TLA+ Specification

   Models the core safety properties of AMTP v0.1 as observed from
   128+ collaboration ticks between CORE@e196 and CASO@MAVK.

   This is a spec for reasoning, not model-checked (yet).
   Runnable property tests are in tests/test_amtp_properties.py.
*)

EXTENDS Naturals, Sequences, FiniteSets

CONSTANTS
    Agents,         \* set of agent callsigns {"CORE", "CASO"}
    MaxSeq,         \* upper bound on seq numbers
    Channels,       \* {0, 1} — public primary vs amigosmalla
    Transports      \* {"lora", "git", "gh", "kbfs", "mqtt"}

VARIABLES
    seq,            \* seq[a] = current seq for agent a
    sent,           \* sent[a] = sequence of messages sent by a
    received,       \* received[a] = set of messages received by a
    decisions,      \* decisions[prop_seq] = {agent votes}
    serial_holder,  \* which agent (or NONE) holds the serial port
    transport_up    \* transport_up[t] = TRUE if transport t is available

vars == <<seq, sent, received, decisions, serial_holder, transport_up>>

TypeOK ==
    /\ seq \in [Agents -> 0..MaxSeq]
    /\ serial_holder \in Agents \cup {"NONE"}
    /\ transport_up \in [Transports -> BOOLEAN]

--------------------------------------------------------------------------
(* C1: Serial Exclusion — at most one agent holds the port *)

SerialExclusion ==
    Cardinality({a \in Agents : serial_holder = a}) <= 1

--------------------------------------------------------------------------
(* C2: Seq Monotonicity — seq only increases, no gaps *)

SeqMonotonic ==
    \A a \in Agents :
        \A i \in 1..Len(sent[a])-1 :
            sent[a][i+1].seq = sent[a][i].seq + 1

--------------------------------------------------------------------------
(* C3: Channel Safety — never send on channel 0 *)

ChannelSafety ==
    \A a \in Agents :
        \A i \in 1..Len(sent[a]) :
            sent[a][i].channel # 0

--------------------------------------------------------------------------
(* C4: Fanout Order — higher priority transports tried first *)
(* lora > git > gh > kbfs > mqtt *)

FanoutOrder ==
    \A a \in Agents :
        \A i \in 1..Len(sent[a]) :
            LET msg == sent[a][i]
            IN transport_up["lora"] => msg.transport = "lora"

--------------------------------------------------------------------------
(* C5: Message Parsability — all messages match AMTP regex *)

MessageParsable ==
    \A a \in Agents :
        \A i \in 1..Len(sent[a]) :
            LET msg == sent[a][i]
            IN /\ Len(msg.agent) >= 2 /\ Len(msg.agent) <= 8
               /\ msg.type \in {"HELO","ACK","PROP","VOTE","SYNC",
                                "TASK","DONE","NOTE","PING"}
               /\ msg.seq >= 1
               /\ Len(msg.raw) <= 200

--------------------------------------------------------------------------
(* C6: Idempotency — duplicate receipt has no effect *)

Idempotent ==
    \A a \in Agents :
        \A msg1, msg2 \in received[a] :
            (msg1.agent = msg2.agent /\ msg1.seq = msg2.seq)
            => msg1 = msg2  \* same message, not counted twice

--------------------------------------------------------------------------
(* C8: Clock Independence — ordering uses (agent, seq) not timestamps *)

ClockIndependent ==
    \A a \in Agents :
        \A i, j \in 1..Len(sent[a]) :
            i < j => sent[a][i].seq < sent[a][j].seq
            \* ordering determined by seq, not wall clock

--------------------------------------------------------------------------
(* C9: Proposal Convergence — every PROP eventually gets voted *)

ProposalConvergence ==
    \A prop_seq \in DOMAIN decisions :
        Cardinality(decisions[prop_seq]) = Cardinality(Agents)
        \* all agents have voted on every proposal

--------------------------------------------------------------------------
(* C10: Log Completeness — every sent message is logged *)

LogComplete ==
    \A a \in Agents :
        \A i \in 1..Len(sent[a]) :
            sent[a][i] \in sent[a]  \* tautological here;
            \* in impl: amtp-log.jsonl must have entry for every send

--------------------------------------------------------------------------
(* Safety: conjunction of all invariants *)

Safety ==
    /\ SerialExclusion
    /\ SeqMonotonic
    /\ ChannelSafety
    /\ MessageParsable
    /\ Idempotent
    /\ ClockIndependent

=============================================================================
