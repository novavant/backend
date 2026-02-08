-- Update kategori dan produk: Router -> Neura, Mifi -> Finora, Powerbank -> Corex
-- Jalankan jika DB masih punya nama lama

-- Update categories
UPDATE categories SET name = 'Neura' WHERE name = 'Router';
UPDATE categories SET name = 'Finora' WHERE name = 'Mifi';
UPDATE categories SET name = 'Corex' WHERE name = 'Powerbank';

-- Update products (sesuai category_id: 1=Neura, 2=Finora, 3=Corex)
UPDATE products SET name = REPLACE(name, 'Router ', 'Neura ') WHERE category_id = 1 AND name LIKE 'Router %';
UPDATE products SET name = REPLACE(name, 'Mifi ', 'Finora ') WHERE category_id = 2 AND name LIKE 'Mifi %';
UPDATE products SET name = REPLACE(name, 'Powerbank ', 'Corex ') WHERE category_id = 3 AND name LIKE 'Powerbank %';

-- Update transactions message jika ada
UPDATE transactions SET message = REPLACE(REPLACE(REPLACE(message, 'Router ', 'Neura '), 'Mifi ', 'Finora '), 'Powerbank ', 'Corex ') 
WHERE message LIKE '%Router %' OR message LIKE '%Mifi %' OR message LIKE '%Powerbank %';
