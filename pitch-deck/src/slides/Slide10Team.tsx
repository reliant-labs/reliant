import { slides } from "../slides";
import { SlideLayout } from "../components/SlideLayout";

/** Slide 10: Team. Thin wrapper — selects its data, renders the layout. */
const data = slides.find((s) => s.id === "team")!;
export default function Slide10Team() {
  return <SlideLayout data={data} />;
}
