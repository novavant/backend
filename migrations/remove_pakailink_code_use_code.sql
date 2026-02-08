-- Untuk DB yang sudah punya kolom pakailink_code: gabungkan ke code, lalu hapus pakailink_code
-- Jalankan hanya jika add_banks_short_name_type.sql (versi lama) sudah dijalankan

-- Copy pakailink_code ke code
UPDATE banks SET code = pakailink_code WHERE pakailink_code IS NOT NULL AND pakailink_code != '';

-- Hapus kolom pakailink_code
ALTER TABLE banks DROP COLUMN pakailink_code;
