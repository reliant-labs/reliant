--
-- PostgreSQL database dump
--

\restrict egH1cOIgIe5fPyJA5EJR20STUb60eLk0Eklk8o95e1AKjno0HZxtoMoUPcOa6IP

-- Dumped from database version 16.15
-- Dumped by pg_dump version 16.15

SET statement_timeout = 0;
SET lock_timeout = 0;
SET idle_in_transaction_session_timeout = 0;
SET client_encoding = 'UTF8';
SET standard_conforming_strings = on;
SELECT pg_catalog.set_config('search_path', '', false);
SET check_function_bodies = false;
SET xmloption = content;
SET client_min_messages = warning;
SET row_security = off;

--
-- Name: plan_kind; Type: TYPE; Schema: public; Owner: -
--

CREATE TYPE public.plan_kind AS ENUM (
    'free',
    'pro'
);


--
-- Name: account_count(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.account_count() RETURNS bigint
    LANGUAGE sql
    AS $$
SELECT count(*) FROM accounts
$$;


--
-- Name: touch(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.touch() RETURNS trigger
    LANGUAGE plpgsql
    AS $_$
BEGIN
SET LOCAL search_path = public;
  NEW.name := NEW.name || $$x$$;
  NEW.note := coalesce(NEW.note, 'a
\restrict not-a-meta');
  RETURN NEW;
END
$_$;


SET default_tablespace = '';

SET default_table_access_method = heap;

--
-- Name: accounts; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.accounts (
    id bigint NOT NULL,
    name text NOT NULL,
    note text
);


--
-- Name: TABLE accounts; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.accounts IS 'customer accounts; SET x = 1';


--
-- Name: accounts_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.accounts_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: accounts_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.accounts_id_seq OWNED BY public.accounts.id;


--
-- Name: plans; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.plans (
    id integer NOT NULL,
    kind public.plan_kind NOT NULL,
    account_id bigint
);


--
-- Name: accounts id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.accounts ALTER COLUMN id SET DEFAULT nextval('public.accounts_id_seq'::regclass);


--
-- Data for Name: accounts; Type: TABLE DATA; Schema: public; Owner: -
--

INSERT INTO public.accounts VALUES (1, 'acmex', 'line one
SET search_path = evil;
\restrict nope
end');


--
-- Data for Name: plans; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Name: accounts_id_seq; Type: SEQUENCE SET; Schema: public; Owner: -
--

SELECT pg_catalog.setval('public.accounts_id_seq', 1, true);


--
-- Name: accounts accounts_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.accounts
    ADD CONSTRAINT accounts_pkey PRIMARY KEY (id);


--
-- Name: plans plans_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.plans
    ADD CONSTRAINT plans_pkey PRIMARY KEY (id);


--
-- Name: accounts accounts_touch; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER accounts_touch BEFORE INSERT ON public.accounts FOR EACH ROW EXECUTE FUNCTION public.touch();


--
-- Name: plans plans_account_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.plans
    ADD CONSTRAINT plans_account_id_fkey FOREIGN KEY (account_id) REFERENCES public.accounts(id);


--
-- PostgreSQL database dump complete
--

\unrestrict egH1cOIgIe5fPyJA5EJR20STUb60eLk0Eklk8o95e1AKjno0HZxtoMoUPcOa6IP

