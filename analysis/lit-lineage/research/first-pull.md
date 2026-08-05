# First pull — OpenAlex traffic-flow corpus

Seed queries: 14, up to 3 pages (600 works) each, relevance-ranked. Raw pages cached in `data/raw/`; merged table in `data/works.jsonl`.

**Unique works: 7264** (year range 1928–2026).

## Per-query counts (unique works contributed)

| seed query | works | unique to this query |
|---|---|---|
| traffic flow theory | 600 | 393 |
| car-following model | 600 | 520 |
| Lighthill Whitham Richards | 600 | 439 |
| kinematic wave traffic | 600 | 371 |
| fundamental diagram traffic flow | 600 | 252 |
| cell transmission model | 600 | 566 |
| macroscopic fundamental diagram | 600 | 418 |
| microscopic traffic simulation | 600 | 517 |
| traffic assignment | 600 | 558 |
| three-phase traffic theory | 600 | 411 |
| intelligent driver model | 600 | 520 |
| lane changing model traffic | 600 | 440 |
| stop-and-go waves | 600 | 537 |
| traffic state estimation | 600 | 480 |

## Publication-decade histogram

```
1920s      1  #
1930s      1  #
1950s      9  #
1960s     53  #
1970s     81  #
1980s    121  #
1990s    431  #####
2000s   1541  ##################
2010s   3380  ########################################
2020s   1641  ###################
```

## Top 30 works by citation count (unfiltered)

Raw relevance pulls catch mega-cited off-field papers — this table documents that contamination; see the filtered table below.

| # | title | year | venue | cited by |
|---|---|---|---|---|
| 1 |  | 2021 | DROPS (Schloss Dagstuhl – Leibniz Center for Informatics) | 50423 |
| 2 | SciPy 1.0: fundamental algorithms for scientific computing in Python | 2020 | Nature Methods | 38808 |
| 3 | QUANTUM ESPRESSO: a modular and open-source software project for quantum simulations of materials | 2009 | Journal of Physics Condensed Matter | 29034 |
| 4 | <b>lmerTest</b> Package: Tests in Linear Mixed Effects Models | 2017 | Journal of Statistical Software | 23891 |
| 5 | A pneumonia outbreak associated with a new coronavirus of probable bat origin | 2020 | Nature | 23385 |
| 6 | Grey Wolf Optimizer | 2014 | Advances in Engineering Software | 18732 |
| 7 | Ferroptosis: An Iron-Dependent Form of Nonapoptotic Cell Death | 2012 | Cell | 18477 |
| 8 | A Multi-Modal Distributed Real-Time IoT System for Urban Traffic Control (Invited Paper) | 2024 | DROPS (Schloss Dagstuhl – Leibniz Center for Informatics) | 14351 |
| 9 | MOLSCRIPT: a program to produce both detailed and schematic plots of protein structures | 1991 | Journal of Applied Crystallography | 14154 |
| 10 | 2013 ESH/ESC Guidelines for the management of arterial hypertension | 2013 | European Heart Journal | 13688 |
| 11 | Ultrastructural Characterization of the Lower Motor System in a Mouse Model of Krabbe Disease | 2016 | Scientific Reports | 13292 |
| 12 | Internet of Things (IoT): A vision, architectural elements, and future directions | 2013 | Future Generation Computer Systems | 12056 |
| 13 | LAMMPS - a flexible simulation tool for particle-based materials modeling at the atomic, meso, and continuum scales | 2021 | Computer Physics Communications | 11940 |
| 14 | First principles phonon calculations in materials science | 2015 | Scripta Materialia | 11473 |
| 15 | The Internationalization Process of the Firm—A Model of Knowledge Development and Increasing Foreign Market Commitments | 1977 | Journal of International Business Studies | 10875 |
| 16 | Dementia prevention, intervention, and care: 2020 report of the Lancet Commission | 2020 | The Lancet | 10434 |
| 17 | Designing qualitative research | 1989 | Choice Reviews Online | 9934 |
| 18 | A familial cluster of pneumonia associated with the 2019 novel coronavirus indicating person-to-person transmission: a study of a family cluster | 2020 | The Lancet | 9625 |
| 19 | Declining Morbidity and Mortality among Patients with Advanced Human Immunodeficiency Virus Infection | 1998 | New England Journal of Medicine | 9518 |
| 20 | Explainable Artificial Intelligence (XAI): Concepts, taxonomies, opportunities and challenges toward responsible AI | 2019 | Information Fusion | 9337 |
| 21 | Partial Least Squares Structural Equation Modeling (PLS-SEM) Using R | 2021 | Classroom companion: business | 8856 |
| 22 | A fast quantum mechanical algorithm for database search | 1996 | None | 8698 |
| 23 | The Health Belief Model: A Decade Later | 1984 | Health Education Quarterly | 8435 |
| 24 | Quantum cryptography | 2002 | Reviews of Modern Physics | 8421 |
| 25 | What Will 5G Be? | 2014 | IEEE Journal on Selected Areas in Communications | 8213 |
| 26 | Prevention of HIV-1 Infection with Early Antiretroviral Therapy | 2011 | New England Journal of Medicine | 6981 |
| 27 | Culture of Human Endothelial Cells Derived from Umbilical Veins. IDENTIFICATION BY MORPHOLOGIC AND IMMUNOLOGIC CRITERIA | 1973 | Journal of Clinical Investigation | 6909 |
| 28 | Massive MIMO for next generation wireless systems | 2014 | IEEE Communications Magazine | 6896 |
| 29 | Technological transitions as evolutionary reconfiguration processes: a multi-level perspective and a case-study | 2002 | Research Policy | 6864 |
| 30 | The Problem of Pattern and Scale in Ecology: The Robert H. MacArthur Award Lecture | 1992 | Ecology | 6805 |

## Top 30 on-topic works by citation count

Filtered to works tagged with a traffic OpenAlex topic (5 topics, see harvest.py `ON_TOPIC_TOPICS`). This is where the classics should surface.

| # | title | year | venue | cited by |
|---|---|---|---|---|
On-topic works: **5248** of 7264.

| 1 | Machine Learning: Algorithms, Real-World Applications and Research Directions | 2021 | SN Computer Science | 5235 |
| 2 | Congested traffic states in empirical observations and microscopic simulations | 2000 | Physical review. E, Statistical physics, plasmas, fluids, and related interdisciplinary topics | 4640 |
| 3 | On kinematic waves II. A theory of traffic flow on long crowded roads | 1955 | Proceedings of the Royal Society of London A Mathematical and Physical Sciences | 4627 |
| 4 | Shock Waves on the Highway | 1956 | Operations Research | 3631 |
| 5 | Spatio-Temporal Graph Convolutional Networks: A Deep Learning Framework for Traffic Forecasting | 2018 | None | 3388 |
| 6 | Microscopic Traffic Simulation using SUMO | 2018 | None | 3165 |
| 7 | The cell transmission model: A dynamic representation of highway traffic consistent with the hydrodynamic theory | 1994 | Transportation Research Part B Methodological | 2916 |
| 8 | Attention Based Spatial-Temporal Graph Convolutional Networks for Traffic Flow Forecasting | 2019 | Proceedings of the AAAI Conference on Artificial Intelligence | 2855 |
| 9 | A behavioural car-following model for computer simulation | 1981 | Transportation Research Part B Methodological | 2339 |
| 10 | Recent Development and Applications of SUMO - Simulation of Urban MObility | 2012 | elib (German Aerospace Center) | 1979 |
| 11 | The cell transmission model, part II: Network traffic | 1995 | Transportation Research Part B Methodological | 1965 |
| 12 | Full velocity difference model for a car-following theory | 2001 | Physical review. E, Statistical physics, plasmas, fluids, and related interdisciplinary topics | 1577 |
| 13 | The Impact of Cooperative Adaptive Cruise Control on Traffic-Flow Characteristics | 2006 | IEEE Transactions on Intelligent Transportation Systems | 1493 |
| 14 | A Multiagent Approach to Autonomous Intersection Management | 2008 | Journal of Artificial Intelligence Research | 1313 |
| 15 | General Lane-Changing Model MOBIL for Car-Following Models | 2007 | Transportation Research Record Journal of the Transportation Research Board | 1308 |
| 16 | Car-following: a historical review | 1999 | Transportation Research Part F Traffic Psychology and Behaviour | 1271 |
| 17 | Review of road traffic control strategies | 2003 | Proceedings of the IEEE | 1230 |
| 18 | SUMO - Simulation of Urban MObility An Overview | 2011 | elib (German Aerospace Center) | 1206 |
| 19 | Generalized force model of traffic dynamics | 1998 | Physical review. E, Statistical physics, plasmas, fluids, and related interdisciplinary topics | 1172 |
| 20 | A model for the structure of lane-changing decisions | 1986 | Transportation Research Part B Methodological | 1151 |
| 21 | AI-Based Modeling: Techniques, Applications and Research Issues Towards Automation, Intelligent and Smart Systems | 2022 | SN Computer Science | 1119 |
| 22 | Modeling cooperative and autonomous adaptive cruise control dynamic responses using experimental data | 2014 | Transportation Research Part C Emerging Technologies | 1089 |
| 23 | The Traffic Assignment Problem: Models and Methods | 1994 | Medical Entomology and Zoology | 1055 |
| 24 | Human mobility: Models and applications | 2018 | Physics Reports | 1038 |
| 25 | The physics of traffic jams | 2002 | Reports on Progress in Physics | 979 |
| 26 | A simplified car-following theory: a lower order model | 2002 | Transportation Research Part B Methodological | 966 |
| 27 | Traffic Equilibrium and Variational Inequalities | 1980 | Transportation Science | 936 |
| 28 | On Stochastic Models of Traffic Assignment | 1977 | Transportation Science | 933 |
| 29 | Impacts of Cooperative Adaptive Cruise Control on Freeway Traffic Flow | 2012 | Transportation Research Record Journal of the Transportation Research Board | 884 |
| 30 | BIOGEME: a free package for the estimation of discrete choice models | 2003 | Infoscience (Ecole Polytechnique Fédérale de Lausanne) | 875 |

## Topics and keywords (top 25 each)

OpenAlex topic | works
|---|---
Traffic control and management | 4597
Transportation Planning and Optimization | 3620
Traffic Prediction and Management Techniques | 2787
Autonomous Vehicle Technology and Safety | 731
Traffic and Road Safety | 728
Evacuation and Crowd Dynamics | 582
Vehicle emissions and performance | 291
Transportation and Mobility Innovations | 278
Urban Transport and Accessibility | 212
Vehicular Ad Hoc Networks (VANETs) | 195
Simulation Techniques and Applications | 138
Fluid Dynamics and Turbulent Flows | 118
Human-Automation Interaction and Safety | 102
Human Mobility and Location-Based Analysis | 98
Network Traffic and Congestion Control | 89
Time Series Analysis and Forecasting | 86
Anomaly Detection Techniques and Applications | 79
Data Management and Algorithms | 71
Cellular Automata and Applications | 59
Video Surveillance and Tracking Methods | 58
Smart Parking Systems Research | 57
Vehicle Dynamics and Control Systems | 56
Complex Systems and Time Series Analysis | 55
Network Security and Intrusion Detection | 53
Pulsars and Gravitational Waves Research | 53

OpenAlex keyword | works
|---|---
Computer science | 6183
Engineering | 3969
Mathematics | 3152
Transport engineering | 2542
Traffic flow (computer networking) | 2289
Artificial intelligence | 2125
Simulation | 2111
Physics | 2107
Computer network | 1396
Flow (mathematics) | 1268
Mathematical optimization | 1197
Real-time computing | 1142
Traffic congestion | 1129
Algorithm | 1112
Statistics | 909
Control (management) | 882
Mechanics | 867
Control theory (sociology) | 806
Traffic generation model | 799
Statistical physics | 709
Mathematical analysis | 671
Computer security | 654
Telecommunications | 644
Automotive engineering | 627
Applied mathematics | 615

## Boundaries observed

Heuristic title scan flags **664 works** as probably about network/data traffic rather than road traffic (title matches `(network|data|internet|packet|...)…traffic`). Sample:

- Making NGSIM Data Usable for Studies on Traffic Flow Theory (2013, Transportation Research Record Journal of the Transportation Research Board)
- Network traffic flow theory: Microscopic simulation experiments on supercomputers (1990, Transportation Research Part A General)
- Attention Based Spatial-Temporal Graph Convolutional Networks for Traffic Flow Forecasting (2019, Proceedings of the AAAI Conference on Artificial Intelligence)
- The cell transmission model, part II: Network traffic (1995, Transportation Research Part B Methodological)
- THREE-DIMENSIONAL RELATIONSHIPS AMONG TRAFFIC FLOW THEORY VARIABLES (1989, Transportation Research Record Journal of the Transportation Research Board)
- Spatio-Temporal Graph Convolutional Networks: A Deep Learning Framework for Traffic Forecasting (2018, None)
- Anomalous Data Detection in Vehicular Networks Using Traffic Flow Theory (2019, None)
- Trajectory data-based traffic flow studies: A revisit (2020, Transportation Research Part C Emerging Technologies)
- Recent advances in ITS, traffic flow theory, and network operations (2016, Transportation Research Part C Emerging Technologies)
- Multiple sources and multiple measures based traffic flow prediction using the chaos theory and support vector regression method (2016, Physica A Statistical Mechanics and its Applications)
- Traffic flow matrix-based graph neural network with attention mechanism for traffic flow prediction (2023, Information Fusion)
- ON THE THEORY OF TRAFFIC FLOW IN NETWORKS (1967, Traffic quarterly)
- A kinematic wave theory of multi-commodity network traffic flow (2012, Transportation Research Part B Methodological)
- PPCA-Based Missing Data Imputation for Traffic Flow Volume: A Systematical Approach (2009, IEEE Transactions on Intelligent Transportation Systems)
- Probabilistic description of traffic flow (2005, Physics Reports)
- Traffic flow data forecasting based on interval type-2 fuzzy sets theory (2016, IEEE/CAA Journal of Automatica Sinica)
- Introduction to Network Traffic Flow Theory (2021, Elsevier eBooks)
- Reliable travel time prediction for freeways: Bridging artificial neural networks and traffic flow theory (2004, Research Repository (Delft University of Technology))
- Diagnosing network-wide traffic anomalies (2004, None)
- Principles of highway engineering and traffic analysis (1990, Choice Reviews Online)
- Big-data-generated traffic flow prediction using deep learning and dempster-shafer theory (2016, None)
- Dynamic Traffic Congestion Simulation and Dissipation Control Based on Traffic Flow Theory Model and Neural Network Data Calibration Algorithm (2017, Complexity)
- Application of Chaos Theory in the Prediction of Motorised Traffic Flows on Urban Networks (2016, Mathematical Problems in Engineering)
- A fully-discrete-state kinetic theory approach to traffic flow on road networks (2014, Mathematical Models and Methods in Applied Sciences)
- Citywide Traffic Volume Estimation Using Trajectory Data (2016, IEEE Transactions on Knowledge and Data Engineering)
- Missing data imputation for traffic flow based on combination of fuzzy neural network and rough set theory (2020, Journal of Intelligent Transportation Systems)
- Catastrophe theory and patterns in 30-second freeway traffic data— Implications for incident detection (1989, Transportation Research Part A General)
- Dynamic Network Traffic Assignment Considered as a Continuous Time Optimal Control Problem (1989, Operations Research)
- Traffic theory and the Internet (2001, IEEE Communications Magazine)
- Traffic phase effects in packet-switched gateways (1991, ACM SIGCOMM Computer Communication Review)
- A Robust and Efficient Method for Fusing Heterogeneous Data from Traffic Sensors on Freeways (2009, Computer-Aided Civil and Infrastructure Engineering)
- Dynamic data-driven local traffic state estimation and prediction (2013, Transportation Research Part C Emerging Technologies)
- Offloading in Internet of Vehicles: A Fog-Enabled Real-Time Traffic Management System (2018, IEEE Transactions on Industrial Informatics)
- Traffic assignment problem for a general network (1969, Journal of Research of the National Bureau of Standards Section B Mathematical Sciences)
- Probability Distributions of Travel Times on Arterial Networks: Traffic Flow and Horizontal Queuing Theory Approach (2012, Transportation Research Board 91st Annual MeetingTransportation Research Board)
- Understanding Traffic Systems: Data Analysis and Presentation (1996, Medical Entomology and Zoology)
- Overview and Principles of Internet Traffic Engineering (2002, None)
- Theory-data dual driven car following model in traffic flow mixed of AVs and HDVs (2024, Transportation Research Part C Emerging Technologies)
- Spatiotemporal Recurrent Convolutional Networks for Traffic Prediction in Transportation Networks (2017, Sensors)
- BotSniffer: Detecting Botnet Command and Control Channels in Network Traffic (2008, Journal of Bioresource Management)

Filter notes for phase 2 (hand-written after reading the flagged sample
and the raw top-30):

1. **Ambiguous seed phrases, not "network traffic", are the biggest
   contaminant.** The raw top-cited list is dominated by mega-cited
   off-field papers caught by phrase overlap: `cell transmission model`
   pulls cell/molecular biology (Ferroptosis, COVID pneumonia, MOLSCRIPT,
   QUANTUM ESPRESSO — "cell"), `three-phase traffic theory` pulls
   generic "three-phase" physics/software (SciPy, 5G), `stop-and-go
   waves` pulls medical guidelines (arterial hypertension),
   `macroscopic fundamental diagram` pulls physics simulation tools
   (LAMMPS, phonon calculations). Title/venue heuristics can't fix this
   cleanly; the OpenAlex **topic** field can — a 5-topic whitelist
   (`ON_TOPIC_TOPICS` in harvest.py) already separates 5,248 plausible
   works and puts LWR 1955, Richards 1956, CTM, Gipps, Treiber in the
   top 10 by citations.
2. **The network/data-traffic heuristic has heavy false positives.**
   Of the 664 flagged works, many are squarely on-topic: Daganzo's
   "The cell transmission model, part II: Network traffic" means *road*
   networks, and "data" matches trajectory/NGSIM/sensor-data papers.
   Genuine contamination in the sample: Internet traffic engineering,
   packet-switched gateways, botnet detection, IEEE Comms Mag pieces.
   Phase 2 should filter on topic + venue, not title regex.
3. **Adjacent-field bleed to decide on deliberately:** evacuation/crowd
   dynamics (582 works), VANETs (195), and a large recent blob of
   graph-neural-network traffic *forecasting* papers (a different field
   from traffic-flow theory despite the shared vocabulary).
4. **Garbage records exist and need exclusion rules:** a blank-title
   DROPS record with a corrupted 50,423 citation count; a duplicate of
   the 2010 "Enhanced intelligent driver model" paper dated 1928;
   HTML tags in some titles (`<b>lmerTest</b>`); venue mis-mappings
   (transportation papers listed under "Medical Entomology and
   Zoology"); 800 works with no venue, 664 with no DOI, 2 with no title.

## Known gaps and API quirks for phase 2

- **Pre-1960 anchors are missing from the pull.** Greenshields 1935
  ("A Study of Traffic Capacity"), Wardrop 1952, Pipes 1953, and
  Newell's 1961 car-following paper did not surface at all — thin
  OpenAlex coverage of those venues/eras, and relevance search ranks
  them below 600 modern results. Phase 2 should anchor the graph by
  fetching known classics directly (by DOI/OpenAlex id), not via search.
- **`referenced_works` coverage is era-dependent:** ~62% of 1960s works,
  78% of 1980s, 91% of 2000s, 94% of 2020s have outgoing citation lists.
  Citation edges *into* the classics will be incomplete; main-path
  analysis must tolerate sparse old references (or backfill from
  `cited_by_api_url` reverse lookups).
- **Dedup hazard:** OpenAlex contains near-duplicate records with
  corrupted years (observed: the 2010 "Enhanced intelligent driver
  model" paper also exists as a 1928 record). Phase 2 dedup should key
  on DOI where present.
- No `select=` field was rejected; all 12 requested fields came back on
  every page. `mailto` polite pool was used (repo `git config
  user.email`). No rate-limit or 5xx retries were triggered across the
  42 requests.
