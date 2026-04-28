ALTER TABLE recording_artifacts
    DROP CONSTRAINT IF EXISTS recording_artifacts_kind_check;

ALTER TABLE recording_artifacts
    ADD CONSTRAINT recording_artifacts_kind_check
        CHECK (kind IN (
            'audio_track',
            'video_track',
            'screen_track',
            'composite_playback',
            'transcript_audio',
            'manifest',
            'ai_manifest',
            'session_bundle'
        ));
