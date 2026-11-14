---
title: Sauti Dispatch Demo
emoji: 🚑
colorFrom: green
colorTo: gray
sdk: docker
app_port: 7860
pinned: false
short_description: Emergency dispatch from basic phones. Synthetic data only.
---

# Sauti Dispatch: demo

Sauti Dispatch lets a person in Kawempe Division, Kampala, report a medical emergency from any phone, including a basic handset with no data and no airtime. It classifies the report red, yellow or green with fixed rules, alerts the nearest trained Village Health Team member (VHT) and community transporter, and keeps a tamper-evident record of every step.

This Space runs the real services (`intake-gw`, `dispatch-worker`, `outbox-relay`) and a private PostgreSQL 18 inside one container.

## Synthetic data only

**Do not enter real names, phone numbers or health information.** Everything here is invented: the responders are labelled "VHT 1" or "Transporter 2", the places are "Test Ward 1" and "Test Market 1", and the phone numbers come from the project's test ranges. The demo connects to no real system: text messages go to a simulator, and no call is placed. Everything is erased when the Space restarts or anyone presses **Reset demo**.

Texts that start with `PENDING` are placeholders. The study's approved clinical and message wording replaces them later. The demo shows them exactly as the system sends them.

## How to use it

1. **Caller phone.** Press **Dial \*384\*1#**, or type `*384*1#` and press **Call**. Answer each screen with the keypad and press **Send**. For a red call, choose 1 English, 1 Yes, 1 Injury, 3 (18 to 59), 2 No, 1 Test Ward 1, 2 Test Clinic 1, and 1 This number. The last screen gives the incident number.
2. **Responder phones.** Within seconds, the alert arrives as a text message in the inbox of each nearby responder. Select a responder, press **Dial \*384\*2#**, and press 1 to accept or 2 to decline. The first VHT to accept gets the job: a VHT who accepts later is told "Already taken", and the other alerted VHTs get a stand-down message. The caller gets "help is on the way".
3. **Dispatch board.** Watch the incident's severity, status, timers T1 to T7, alerts, escalations and event timeline update. The caller's number is never shown. Press **Verify audit ledger** to sign a checkpoint and check the whole hash chain.

Test Wards 1 and 2 have responders. Test Wards 3 and 4 have none: a call from there shows the escalation to a coordinator.

## What this demo leaves out

Voice calls (the toll-free line, voice alerts and the masked call bridge), the coordinator console, arrival tracking and payments are not part of this demo.
