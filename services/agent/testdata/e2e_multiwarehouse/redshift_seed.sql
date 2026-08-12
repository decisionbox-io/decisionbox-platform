-- TICKIT sample schema + read-only user for the multi-warehouse E2E (#161).
-- Run as a Redshift superuser against the dbx-discovery serverless workgroup
-- (db `dev`). Idempotent: drops+recreates the TICKIT tables and re-grants.
--
--   psql -h <ep> -p 5439 -U <superuser> -d dev \
--        -v ro_password="<generated>" -f redshift_seed.sql
--
-- COPY pulls the public TICKIT fixtures from s3://redshift-downloads/tickit/
-- using the namespace's attached copy role. `date` is a reserved word → quoted.

DROP TABLE IF EXISTS sales, listing, event, "date", category, venue, users CASCADE;

CREATE TABLE users (
  userid       integer not null distkey sortkey,
  username     char(8),
  firstname    varchar(30),
  lastname     varchar(30),
  city         varchar(30),
  state        char(2),
  email        varchar(100),
  phone        char(14),
  likesports   boolean, liketheatre boolean, likeconcerts boolean, likejazz boolean,
  likeclassical boolean, likeopera boolean, likerock boolean, likevegas boolean,
  likebroadway boolean, likemusicals boolean);

CREATE TABLE venue (
  venueid   smallint not null distkey sortkey,
  venuename varchar(100), venuecity varchar(30), venuestate char(2), venueseats integer);

CREATE TABLE category (
  catid    smallint not null distkey sortkey,
  catgroup varchar(10), catname varchar(10), catdesc varchar(50));

CREATE TABLE "date" (
  dateid  smallint not null distkey sortkey,
  caldate date not null, day character(3) not null, week smallint not null,
  month character(5) not null, qtr character(5) not null, year smallint not null,
  holiday boolean default('N'));

CREATE TABLE event (
  eventid   integer not null distkey,
  venueid   smallint not null, catid smallint not null,
  dateid    smallint not null sortkey,
  eventname varchar(200), starttime timestamp);

CREATE TABLE listing (
  listid        integer not null distkey,
  sellerid      integer not null, eventid integer not null,
  dateid        smallint not null sortkey,
  numtickets    smallint not null, priceperticket decimal(8,2),
  totalprice    decimal(8,2), listtime timestamp);

CREATE TABLE sales (
  salesid    integer not null,
  listid     integer not null distkey,
  sellerid   integer not null, buyerid integer not null, eventid integer not null,
  dateid     smallint not null sortkey,
  qtysold    smallint not null, pricepaid decimal(8,2), commission decimal(8,2),
  saletime   timestamp);

COPY users    FROM 's3://redshift-downloads/tickit/allusers_pipe.txt'   IAM_ROLE 'arn:aws:iam::435998721348:role/dbx-redshift-copy' DELIMITER '|' REGION 'us-east-1';
COPY venue    FROM 's3://redshift-downloads/tickit/venue_pipe.txt'      IAM_ROLE 'arn:aws:iam::435998721348:role/dbx-redshift-copy' DELIMITER '|' REGION 'us-east-1';
COPY category FROM 's3://redshift-downloads/tickit/category_pipe.txt'   IAM_ROLE 'arn:aws:iam::435998721348:role/dbx-redshift-copy' DELIMITER '|' REGION 'us-east-1';
COPY "date"   FROM 's3://redshift-downloads/tickit/date2008_pipe.txt'   IAM_ROLE 'arn:aws:iam::435998721348:role/dbx-redshift-copy' DELIMITER '|' REGION 'us-east-1';
COPY event    FROM 's3://redshift-downloads/tickit/allevents_pipe.txt'  IAM_ROLE 'arn:aws:iam::435998721348:role/dbx-redshift-copy' DELIMITER '|' TIMEFORMAT 'YYYY-MM-DD HH:MI:SS' REGION 'us-east-1';
COPY listing  FROM 's3://redshift-downloads/tickit/listings_pipe.txt'   IAM_ROLE 'arn:aws:iam::435998721348:role/dbx-redshift-copy' DELIMITER '|' REGION 'us-east-1';
COPY sales    FROM 's3://redshift-downloads/tickit/sales_tab.txt'       IAM_ROLE 'arn:aws:iam::435998721348:role/dbx-redshift-copy' DELIMITER '\t' TIMEFORMAT 'MM/DD/YYYY HH:MI:SS' REGION 'us-east-1';

-- Read-only serving user for DecisionBox (ValidateReadOnly must pass).
DROP TABLE IF EXISTS dbx_ro_should_fail;
DROP USER IF EXISTS dbx_ro;
CREATE USER dbx_ro PASSWORD :'ro_password';
GRANT USAGE ON SCHEMA public TO dbx_ro;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO dbx_ro;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT ON TABLES TO dbx_ro;

-- Genuinely read-only: Redshift grants CREATE on the public schema + CREATE/TEMP
-- on the database to PUBLIC by default, so a plain user can still CREATE TABLE.
-- Strip those so dbx_ro (and PUBLIC on this test db) cannot write. Superuser and
-- object owners are unaffected.
REVOKE CREATE ON SCHEMA public FROM PUBLIC;
REVOKE CREATE ON SCHEMA public FROM dbx_ro;
REVOKE CREATE, TEMPORARY ON DATABASE dev FROM PUBLIC;
REVOKE CREATE, TEMPORARY ON DATABASE dev FROM dbx_ro;
