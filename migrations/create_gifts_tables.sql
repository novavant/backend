-- Gift system (dana kaget) - users can share money with others

CREATE TABLE IF NOT EXISTS gifts (
    id INT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    user_id INT NOT NULL,
    code VARCHAR(12) NOT NULL,
    amount DECIMAL(15,2) NOT NULL COMMENT 'total for random, per-winner for equal',
    winner_count INT NOT NULL,
    distribution_type ENUM('random','equal') NOT NULL,
    recipient_type ENUM('all','referral_only') NOT NULL,
    status ENUM('active','completed','expired','cancelled') DEFAULT 'active',
    total_deducted DECIMAL(15,2) NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE KEY uk_gifts_code (code),
    INDEX idx_gifts_user (user_id),
    INDEX idx_gifts_status (status),
    INDEX idx_gifts_created (created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS gift_amount_slots (
    id INT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    gift_id INT UNSIGNED NOT NULL,
    slot_index INT NOT NULL,
    amount DECIMAL(15,2) NOT NULL,
    INDEX idx_gift_slots_gift (gift_id),
    FOREIGN KEY (gift_id) REFERENCES gifts(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS gift_claims (
    id INT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    gift_id INT UNSIGNED NOT NULL,
    user_id INT NOT NULL,
    amount DECIMAL(15,2) NOT NULL,
    slot_index INT NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    INDEX idx_claims_gift (gift_id),
    INDEX idx_claims_user (user_id),
    FOREIGN KEY (gift_id) REFERENCES gifts(id) ON DELETE CASCADE,
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
