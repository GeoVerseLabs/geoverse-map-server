CREATE TABLE IF NOT EXISTS warehouse (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  name VARCHAR(120) NOT NULL,
  address VARCHAR(255) NOT NULL,
  capacity INT NOT NULL,
  location POINT NOT NULL SRID 4326,
  PRIMARY KEY (id),
  SPATIAL INDEX idx_warehouse_location (location)
) ENGINE = InnoDB;

INSERT INTO warehouse (name, address, capacity, location)
VALUES
  (
    '浦东仓',
    '上海市浦东新区',
    1200,
    ST_GeomFromText('POINT(121.5893 31.2047)', 4326, 'axis-order=long-lat')
  ),
  (
    '虹桥仓',
    '上海市闵行区',
    800,
    ST_GeomFromText('POINT(121.3269 31.1979)', 4326, 'axis-order=long-lat')
  ),
  (
    '临港仓',
    '上海市浦东新区临港',
    1600,
    ST_GeomFromText('POINT(121.9100 30.9000)', 4326, 'axis-order=long-lat')
  );
