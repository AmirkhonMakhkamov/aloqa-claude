-- Re-diversify avatar colours: the original 26-colour palette (migration 052)
-- clustered many near-identical greens/blues, so alphabet-adjacent initials and
-- common names painted with hard-to-tell-apart chips. This replaces it with a
-- perceptually distinct palette (hues spread across the wheel, tiered lightness,
-- white-text contrast >= 4.5) and backfills existing users. Keep in sync with
-- internal/domain/entity/avatar_color.go and the frontend fallback
-- (packages/core/src/theme/tokens.ts:avatarPalette).

ALTER TABLE users
    ALTER COLUMN avatar_color SET DEFAULT '#C92642';

WITH palette(colors) AS (
    VALUES (ARRAY[
        '#C92642', '#298113', '#2D75BE', '#A926C9', '#CA4E2F', '#18813F',
        '#1D2DBF', '#CA2FB1', '#976817', '#1F847C', '#5729D6', '#B11B52',
        '#5E8118', '#15638A', '#9235D0', '#B11B1B', '#1F8427', '#295DD6',
        '#AC1BB1', '#B05D21', '#13815C', '#4D41D2', '#C9268E', '#84771F',
        '#1C8292', '#571BB1'
    ]::text[])
)
UPDATE users
SET avatar_color = CASE upper(substring(trim(display_name) from 1 for 1))
    WHEN 'A' THEN '#C92642'
    WHEN 'B' THEN '#298113'
    WHEN 'C' THEN '#2D75BE'
    WHEN 'D' THEN '#A926C9'
    WHEN 'E' THEN '#CA4E2F'
    WHEN 'F' THEN '#18813F'
    WHEN 'G' THEN '#1D2DBF'
    WHEN 'H' THEN '#CA2FB1'
    WHEN 'I' THEN '#976817'
    WHEN 'J' THEN '#1F847C'
    WHEN 'K' THEN '#5729D6'
    WHEN 'L' THEN '#B11B52'
    WHEN 'M' THEN '#5E8118'
    WHEN 'N' THEN '#15638A'
    WHEN 'O' THEN '#9235D0'
    WHEN 'P' THEN '#B11B1B'
    WHEN 'Q' THEN '#1F8427'
    WHEN 'R' THEN '#295DD6'
    WHEN 'S' THEN '#AC1BB1'
    WHEN 'T' THEN '#B05D21'
    WHEN 'U' THEN '#13815C'
    WHEN 'V' THEN '#4D41D2'
    WHEN 'W' THEN '#C9268E'
    WHEN 'X' THEN '#84771F'
    WHEN 'Y' THEN '#1C8292'
    WHEN 'Z' THEN '#571BB1'
    ELSE palette.colors[(get_byte(decode(substr(md5(trim(display_name)), 1, 2), 'hex'), 0) % 26) + 1]
END
FROM palette;
