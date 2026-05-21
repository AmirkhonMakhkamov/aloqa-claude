# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.12.0] - 2026-05-21

### Features

- **chat**: add message attachment and voice support
- **chat**: add workspace directory endpoint with search and filters
- **channels**: add member management endpoints for invite/remove operations
- **channels**: support initial private channel members during creation
- **saved-messages**: add endpoints and user preferences for saved messages
- **ws**: enrich self-channel message events with routing metadata
- **notifications**: add token registration routes for push notifications
- **messages**: accept before pagination cursor for backward traversal
- **calls**: add TURN credential endpoint for WebRTC connection fallback

### Bug Fixes

- **chat**: read full workspace directory pages (pagination support)
- **entity**: allow nullable topic in Channel entity for saved channels
- **api**: return complete embedded users in responses
- **messages**: return persisted reactions with message data
- **chat**: align reaction and forward contracts with frontend
- **auth**: account deactivate and reactivate login support
- **config**: load local dotenv for dev runs
- **demo**: handle nullable channel topic in test fixtures

### Internal

- **db**: saved-messages schema with owner_user_id tracking
- **domain**: saved channel types and SavedFrom field types

[0.12.0]: https://github.com/AmirkhonMakhkamov/aloqa-claude/releases/tag/v0.12.0
