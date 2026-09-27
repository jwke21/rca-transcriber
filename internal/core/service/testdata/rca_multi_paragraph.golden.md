# Incident 77: Checkout latency from exhausted database connections

**Date:** 2026-09-28

**Author:** RCA Transcriber

## Summary

Checkout p99 latency rose above 4 seconds at 09:10 UTC.

The engineer found the orders database connection pool at its limit of 50.
A batch export job had been holding connections open.

The export job was paused at 09:40 and latency recovered by 09:45.
