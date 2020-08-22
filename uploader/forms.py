from django import forms
from apps.main.models import Iban, Bic, BookingType, Currency, Optionee, Transaction


class FileUploadForm(forms.Form):
    choices_bank = [
        ("HAS", "Hamburger Sparkasse"),
        ("DKB", "Deutsche Kredit Bank"),
    ]

    bank = forms.ChoiceField(choices=choices_bank, required=False)
    file = forms.FileField(required=False)
    iban = forms.CharField(max_length=24, strip=True)
    currency = forms.CharField(max_length=3, strip=True)
    booking_type = forms.CharField(max_length=50, strip=True)
    optionee_name = forms.CharField(max_length=200, strip=True)
    optionee_iban = forms.CharField(max_length=22, strip=True)
    optionee_bic = forms.CharField(max_length=11, strip=True)
    date_booking = forms.DateField()
    figure = forms.DecimalField(max_digits=18, decimal_places=2)
    reference = forms.CharField(max_length=500, strip=True)

    def save(self):
        data = self.cleaned_data

        iban, created = Iban.objects.get_or_create(iban=data["iban"])
        if created:
            iban.save()

        o_iban, created = Iban.objects.get_or_create(iban=data["optionee_iban"])
        if created:
            o_iban.save()

        bic, created = Bic.objects.get_or_create(bic=data["optionee_bic"])
        if created:
            bic.save()

        b_type, created = BookingType.objects.get_or_create(name=data["booking_type"])
        if created:
            b_type.save()

        curr, created = Currency.objects.get_or_create(currency_short=data["currency"])
        if created:
            curr.save()

        opt, created = Optionee.objects.get_or_create(name=data["optionee_name"], iban=o_iban, bic=bic)
        if created:
            opt.save()

        transaction, created = Transaction.objects.get_or_create(
            account=iban,
            date_booking=data["date_booking"],
            booking_type=b_type,
            optionee=opt,
            figure=data["figure"],
            currency=curr,
            reference=data["reference"],
        )
        if created:
            transaction.save()
