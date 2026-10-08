# Termes canoniques en anglais, prose en français

Chaque terme du glossaire a un nom canonique anglais (`Pass`, `Shift`, `Opt-in`…), employé dans le code, les ressources Kubernetes et la documentation publique en anglais. Il a aussi un alias français (passe, poste, adhésion…), employé dans la prose française des ADR, des specs et des tickets. Le README public est en anglais, et les CRD de l'opérateur doivent porter des noms anglais. Si le glossaire restait seulement en français, il faudrait traduire en permanence entre le vocabulaire du domaine et celui du code, pour les humains comme pour les agents. À l'inverse, imposer les termes anglais dans des phrases françaises (« le Shift », « la Pass ») aurait demandé de réécrire tout l'existant pour une lecture moins naturelle.

## Considered Options

- **Tout en anglais** (glossaire, ADR, tickets) : écarté. Le mainteneur rédige en français, et tout l'historique l'est.
- **Termes anglais même dans la prose française** : écarté. Il aurait fallu réécrire les ADR et une vingtaine de tickets.
- **Statu quo, tout en français sauf le README** : écarté, à cause de l'écart avec les noms dans le code.

## Consequences

Le glossaire `GLOSSARY.md` fait foi pour la correspondance entre les deux langues. Un identifiant de code ou un kind de CRD prend toujours le nom canonique anglais.
