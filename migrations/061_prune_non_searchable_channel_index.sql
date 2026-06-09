DELETE FROM search_index_jobs AS sij
USING channels AS ch
WHERE sij.resource_type = 'channel'
    AND sij.resource_id = ch.id
    AND (
        ch.archived = true
        OR ch.type NOT IN ('public', 'private')
        OR NULLIF(BTRIM(ch.name), '') IS NULL
    );

DELETE FROM search_index AS si
USING channels AS ch
WHERE si.resource_type = 'channel'
    AND si.resource_id = ch.id
    AND (
        ch.archived = true
        OR ch.type NOT IN ('public', 'private')
        OR NULLIF(BTRIM(ch.name), '') IS NULL
    );
