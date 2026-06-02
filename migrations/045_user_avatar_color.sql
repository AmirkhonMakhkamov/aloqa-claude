ALTER TABLE users
    ADD COLUMN IF NOT EXISTS avatar_color text NOT NULL DEFAULT '#2D7D6A';

WITH palette(colors) AS (
    VALUES (ARRAY[
        '#2D7D6A', '#7C3AED', '#D97706', '#2454D8', '#DB2777', '#059669',
        '#9333EA', '#0EA5E9', '#DC2626', '#16A34A', '#CA8A04', '#C026D3',
        '#0891B2', '#4F46E5', '#BE123C', '#047857', '#B45309', '#4338CA',
        '#0F766E', '#A21CAF', '#1D4ED8', '#B91C1C', '#15803D', '#854D0E',
        '#6D28D9', '#0369A1'
    ]::text[])
)
UPDATE users
SET avatar_color = CASE upper(substring(trim(display_name) from 1 for 1))
    WHEN 'A' THEN '#2D7D6A'
    WHEN 'B' THEN '#7C3AED'
    WHEN 'C' THEN '#D97706'
    WHEN 'D' THEN '#2454D8'
    WHEN 'E' THEN '#DB2777'
    WHEN 'F' THEN '#059669'
    WHEN 'G' THEN '#9333EA'
    WHEN 'H' THEN '#0EA5E9'
    WHEN 'I' THEN '#DC2626'
    WHEN 'J' THEN '#16A34A'
    WHEN 'K' THEN '#CA8A04'
    WHEN 'L' THEN '#C026D3'
    WHEN 'M' THEN '#0891B2'
    WHEN 'N' THEN '#4F46E5'
    WHEN 'O' THEN '#BE123C'
    WHEN 'P' THEN '#047857'
    WHEN 'Q' THEN '#B45309'
    WHEN 'R' THEN '#4338CA'
    WHEN 'S' THEN '#0F766E'
    WHEN 'T' THEN '#A21CAF'
    WHEN 'U' THEN '#1D4ED8'
    WHEN 'V' THEN '#B91C1C'
    WHEN 'W' THEN '#15803D'
    WHEN 'X' THEN '#854D0E'
    WHEN 'Y' THEN '#6D28D9'
    WHEN 'Z' THEN '#0369A1'
    ELSE palette.colors[(get_byte(decode(substr(md5(trim(display_name)), 1, 2), 'hex'), 0) % 26) + 1]
END
FROM palette;
